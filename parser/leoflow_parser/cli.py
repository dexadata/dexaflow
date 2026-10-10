"""Command-line entry point for the Dexaflow DAG parser."""
from __future__ import annotations

import argparse
import json
import sys

from .compiler import compile_dag


def main(argv: list[str] | None = None) -> int:
    """Parse arguments and run the requested parser subcommand."""
    parser = argparse.ArgumentParser(prog="leoflow_parser")
    sub = parser.add_subparsers(dest="command", required=True)

    compile_cmd = sub.add_parser("compile", help="compile a DAG into dag.json")
    compile_cmd.add_argument("--source", required=True, help="path to the DAG Python file")
    compile_cmd.add_argument("--config", required=True, help="path to dexaflow.yaml")
    compile_cmd.add_argument("--output", required=True, help="path to write dag.json")
    compile_cmd.add_argument("--image", required=True, help="container image reference")
    compile_cmd.add_argument("--dag-version", default="dev", help="DAG version label")

    args = parser.parse_args(argv)
    if args.command == "compile":
        spec = compile_dag(args.source, args.config, args.image, args.dag_version)
        with open(args.output, "w") as handle:
            json.dump(spec, handle, indent=2)
        print(f"Wrote {args.output}")
        return 0
    return 1


if __name__ == "__main__":
    sys.exit(main())
