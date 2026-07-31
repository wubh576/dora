package domain

import "time"

const (
	QuotaWindowFiveHour = "five_hour"
	QuotaWindowSevenDay = "seven_day"
)

type QuotaSnapshot struct {
	Provider         string
	WindowKey        string
	Label            string
	UsedPercent      float64
	RemainingPercent float64
	ResetsAt         *time.Time
	FetchedAt        time.Time
	Source           string
	SourceState      string
	Plan             string
	AccountLabel     string
}

type QuotaProviderState struct {
	Status      string
	LastQuotaAt *time.Time
	LastError   string
}
