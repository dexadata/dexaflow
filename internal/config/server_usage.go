package config

// UsageSection configures the per-tenant usage records of ADR 0096.
type UsageSection struct {
	Events UsageEventsSection `mapstructure:"events"`
}

// UsageEventsSection configures the task attempt usage events.
type UsageEventsSection struct {
	// Enabled turns on the recording of one usage event per finished task
	// attempt. The scheduler leader writes it to the database, where the
	// trigger of migration 055 reads it, so every replica records the same way.
	Enabled bool `mapstructure:"enabled"`
}
