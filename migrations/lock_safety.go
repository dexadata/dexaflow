package migrations

import (
	"regexp"
	"strings"
)

// The migration lock check (#2312).
//
// A migration that takes an ACCESS EXCLUSIVE lock on a large table and then
// rewrites or scans it (a non-concurrent index, a type change, a constraint
// validated in place) stalls every scheduler tick and every API call that
// touches the table for as long as it runs on a production-sized database.
// Migration timing was measured only at rc review, so such a file could merge
// and ship before anyone saw it. LockFindings reads a migration's SQL and names
// each statement that does one of those things on a table listed in
// LargeTables, with the safe pattern to use instead; the test in
// lock_safety_test.go fails on every finding of a migration newer than
// lockSafetyBaseline that lock_exceptions.txt does not justify.
//
// It is a pattern check on the SQL text, not a parser: it is meant to catch
// the common shapes and to make an exception a reviewed, written decision.

// LargeTables are the tables that grow with the work an installation runs, so
// a lock held while scanning or rewriting them grows too. The other tables
// (users, roles, pools, dags and the like) stay small.
var LargeTables = []string{
	"task_instances",
	"dag_runs",
	"task_instance_history",
	"task_state_history",
	"xcom_index",
	"xcom_store",
	"audit_log",
	"usage_events",
	"dag_versions",
}

// LockFinding is one statement of a migration that locks a large table
// unsafely.
type LockFinding struct {
	// Rule names the pattern, as lock_exceptions.txt refers to it.
	Rule string
	// Table is the large table the statement locks.
	Table string
	// Statement is the statement, whitespace collapsed.
	Statement string
	// Fix says what to write instead.
	Fix string
}

type lockRule struct {
	name string
	// match reports whether a statement, upper-cased and whitespace collapsed,
	// breaks the rule.
	match func(stmt string) bool
	fix   string
}

var (
	reCreateIndex     = regexp.MustCompile(`^CREATE (UNIQUE )?INDEX `)
	reAlterTable      = regexp.MustCompile(`^ALTER TABLE `)
	reAddColumn       = regexp.MustCompile(`\bADD (COLUMN )?(IF NOT EXISTS )?[A-Z_][A-Z0-9_]* `)
	reVolatileDefault = regexp.MustCompile(`\bDEFAULT [^,]*\b(GEN_RANDOM_UUID|UUID_GENERATE_V4|RANDOM|CLOCK_TIMESTAMP|TIMEOFDAY|NEXTVAL|DEXAFLOW_UUIDV7)\s*\(`)
	reAddConstraint   = regexp.MustCompile(`\bADD (CONSTRAINT [A-Z_][A-Z0-9_]* )?(CHECK|FOREIGN KEY)\b`)
	reAddUnique       = regexp.MustCompile(`\bADD (CONSTRAINT [A-Z_][A-Z0-9_]* )?(PRIMARY KEY|UNIQUE)\b`)
	reTableName       = regexp.MustCompile(`^[A-Z_][A-Z0-9_.]*`)
	reCreateTable     = regexp.MustCompile(`^CREATE (UNLOGGED )?TABLE (IF NOT EXISTS )?([A-Z_][A-Z0-9_.]*)`)
	reTypeChange      = regexp.MustCompile(`\bALTER (COLUMN )?[A-Z_][A-Z0-9_]* (SET DATA )?TYPE\b`)
)

var lockRules = []lockRule{
	{
		name: "index-not-concurrent",
		match: func(s string) bool {
			return reCreateIndex.MatchString(s) && !strings.Contains(s, " CONCURRENTLY ")
		},
		fix: "CREATE INDEX CONCURRENTLY in a file of its own, without BEGIN/COMMIT and without IF NOT EXISTS (see 054)",
	},
	{
		name: "reindex-not-concurrent",
		match: func(s string) bool {
			return strings.HasPrefix(s, "REINDEX ") && !strings.Contains(s, "CONCURRENTLY")
		},
		fix: "REINDEX ... CONCURRENTLY in a file of its own",
	},
	{
		name: "rewrite",
		match: func(s string) bool {
			return strings.HasPrefix(s, "VACUUM FULL") || strings.HasPrefix(s, "CLUSTER ") ||
				(reAlterTable.MatchString(s) && (strings.Contains(s, " SET LOGGED") || strings.Contains(s, " SET UNLOGGED")))
		},
		fix: "do not rewrite a large table in a migration; plan it as an operator step",
	},
	{
		name: "column-type-change",
		match: func(s string) bool {
			return reAlterTable.MatchString(s) && reTypeChange.MatchString(s)
		},
		fix: "add a new column, backfill it in batches from the application, and switch readers over (expand/contract, ADR 0090)",
	},
	{
		name: "add-column-not-null-without-default",
		match: func(s string) bool {
			return reAlterTable.MatchString(s) && reAddColumn.MatchString(s) &&
				strings.Contains(s, " NOT NULL") && !strings.Contains(s, " DEFAULT ")
		},
		fix: "add the column nullable or with a constant default (expand/contract, ADR 0090)",
	},
	{
		name: "add-column-volatile-default",
		match: func(s string) bool {
			return reAlterTable.MatchString(s) && reAddColumn.MatchString(s) && reVolatileDefault.MatchString(s)
		},
		fix: "a volatile default rewrites every row; add the column without it, set the default in a second statement, and backfill in batches",
	},
	{
		name: "set-not-null",
		match: func(s string) bool {
			return reAlterTable.MatchString(s) && strings.Contains(s, " SET NOT NULL")
		},
		fix: "add a CHECK (col IS NOT NULL) NOT VALID constraint, VALIDATE it in a later statement, then SET NOT NULL, which Postgres 12+ proves from the valid constraint without a scan",
	},
	{
		name: "constraint-validated-in-place",
		match: func(s string) bool {
			return reAlterTable.MatchString(s) && reAddConstraint.MatchString(s) && !strings.Contains(s, " NOT VALID")
		},
		fix: "ADD CONSTRAINT ... NOT VALID, then ALTER TABLE ... VALIDATE CONSTRAINT in its own transaction (it takes only SHARE UPDATE EXCLUSIVE)",
	},
	{
		name: "unique-without-index",
		match: func(s string) bool {
			return reAlterTable.MatchString(s) && reAddUnique.MatchString(s) && !strings.Contains(s, " USING INDEX ")
		},
		fix: "build the unique index CONCURRENTLY first, then ADD CONSTRAINT ... UNIQUE USING INDEX (or PRIMARY KEY USING INDEX)",
	},
	{
		name: "lock-table",
		match: func(s string) bool {
			return strings.HasPrefix(s, "LOCK ")
		},
		fix: "do not take an explicit table lock on a large table in a migration",
	},
	{
		name: "full-table-write",
		match: func(s string) bool {
			if !strings.HasPrefix(s, "UPDATE ") && !strings.HasPrefix(s, "DELETE FROM ") {
				return false
			}
			return !strings.Contains(s, " WHERE ")
		},
		fix: "backfill in batches from the application, or bound the statement with a WHERE on an indexed column",
	},
}

// LockFindings returns every statement of sql that locks a large table
// unsafely (a table the same file creates is not counted, since it is empty), and, when the file takes any lock on a large table with ALTER
// TABLE, a finding if it never bounds the lock wait with SET LOCAL
// lock_timeout (migrations/AGENTS.md, "Bound lock waits").
func LockFindings(sql string) []LockFinding {
	var out []LockFinding
	altersLarge := false
	hasLockTimeout := false
	// A table the same file creates is empty while the file runs, so nothing
	// done to it there can hold a lock for long.
	created := map[string]bool{}
	for _, raw := range splitStatements(sql) {
		stmt := normalizeStatement(raw)
		if stmt == "" {
			continue
		}
		if m := reCreateTable.FindStringSubmatch(stmt); m != nil {
			created[m[3]] = true
			continue
		}
		if strings.HasPrefix(stmt, "SET LOCAL LOCK_TIMEOUT") || strings.HasPrefix(stmt, "SET LOCK_TIMEOUT") {
			hasLockTimeout = true
		}
		table := statementTable(stmt)
		if table == "" || !isLargeTable(table) || created[table] {
			continue
		}
		if reAlterTable.MatchString(stmt) {
			altersLarge = true
		}
		for _, r := range lockRules {
			if r.match(stmt) {
				out = append(out, LockFinding{Rule: r.name, Table: table, Statement: stmt, Fix: r.fix})
			}
		}
	}
	if altersLarge && !hasLockTimeout {
		out = append(out, LockFinding{
			Rule: "no-lock-timeout",
			Fix:  "run the ALTER TABLE in a transaction with SET LOCAL lock_timeout (see 048)",
		})
	}
	return out
}

func isLargeTable(name string) bool {
	name = strings.ToLower(strings.TrimPrefix(strings.ToLower(name), "public."))
	for _, t := range LargeTables {
		if name == t {
			return true
		}
	}
	return false
}

// statementTable returns the table a statement locks, upper-cased, or "" when
// the statement locks no table this check knows about.
func statementTable(s string) string {
	var rest string
	switch {
	case reCreateIndex.MatchString(s):
		i := strings.Index(s, " ON ")
		if i < 0 {
			return ""
		}
		rest = strings.TrimPrefix(s[i+len(" ON "):], "ONLY ")
	case reAlterTable.MatchString(s):
		rest = strings.TrimPrefix(s, "ALTER TABLE ")
		rest = strings.TrimPrefix(rest, "IF EXISTS ")
		rest = strings.TrimPrefix(rest, "ONLY ")
	case strings.HasPrefix(s, "REINDEX TABLE "):
		rest = strings.TrimPrefix(strings.TrimPrefix(s, "REINDEX TABLE "), "CONCURRENTLY ")
	case strings.HasPrefix(s, "VACUUM FULL "):
		rest = strings.TrimPrefix(s, "VACUUM FULL ")
	case strings.HasPrefix(s, "CLUSTER "):
		rest = strings.TrimPrefix(s, "CLUSTER ")
	case strings.HasPrefix(s, "LOCK TABLE "):
		rest = strings.TrimPrefix(strings.TrimPrefix(s, "LOCK TABLE "), "ONLY ")
	case strings.HasPrefix(s, "LOCK "):
		rest = strings.TrimPrefix(strings.TrimPrefix(s, "LOCK "), "ONLY ")
	case strings.HasPrefix(s, "UPDATE "):
		rest = strings.TrimPrefix(strings.TrimPrefix(s, "UPDATE "), "ONLY ")
	case strings.HasPrefix(s, "DELETE FROM "):
		rest = strings.TrimPrefix(strings.TrimPrefix(s, "DELETE FROM "), "ONLY ")
	default:
		return ""
	}
	return reTableName.FindString(rest)
}

// normalizeStatement upper-cases a statement and collapses its whitespace,
// keeping a trailing space so word matches at the end work.
func normalizeStatement(s string) string {
	f := strings.Fields(strings.ToUpper(s))
	if len(f) == 0 {
		return ""
	}
	return strings.Join(f, " ") + " "
}

// splitStatements splits SQL on the semicolons that end statements, after
// dropping comments, and leaves semicolons inside quotes and dollar-quoted
// bodies (a function, a DO block) alone.
func splitStatements(sql string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			j := strings.IndexByte(sql[i:], '\n')
			if j < 0 {
				i = len(sql)
				continue
			}
			i += j
		case strings.HasPrefix(sql[i:], "/*"):
			j := strings.Index(sql[i+2:], "*/")
			if j < 0 {
				i = len(sql)
				continue
			}
			i += j + 4
			cur.WriteByte(' ')
		case sql[i] == '\'':
			j := closingQuote(sql, i)
			cur.WriteString(sql[i:j])
			i = j
		case sql[i] == '$':
			tag := dollarTag(sql[i:])
			if tag == "" {
				cur.WriteByte(sql[i])
				i++
				continue
			}
			j := strings.Index(sql[i+len(tag):], tag)
			end := len(sql)
			if j >= 0 {
				end = i + len(tag) + j + len(tag)
			}
			// The body is opaque to this check: keep a placeholder.
			cur.WriteString(" $BODY$ ")
			i = end
		case sql[i] == ';':
			out = append(out, cur.String())
			cur.Reset()
			i++
		default:
			cur.WriteByte(sql[i])
			i++
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}

// closingQuote returns the index just past the single-quoted literal that
// starts at i, with ” as an escaped quote.
func closingQuote(sql string, i int) int {
	for j := i + 1; j < len(sql); j++ {
		if sql[j] != '\'' {
			continue
		}
		if j+1 < len(sql) && sql[j+1] == '\'' {
			j++
			continue
		}
		return j + 1
	}
	return len(sql)
}

var reDollarTag = regexp.MustCompile(`^\$[A-Za-z_]*\$`)

// dollarTag returns the opening dollar-quote tag at the start of s ($$ or
// $name$), or "" when s does not start one.
func dollarTag(s string) string {
	return reDollarTag.FindString(s)
}
