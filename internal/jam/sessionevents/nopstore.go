package sessionevents

import "time"

// NopStore is the "no storage configured" backend: events are accepted (and
// therefore acked) and dropped, so coves never back up.
type NopStore struct{}

func (NopStore) Append(Event) error                       { return nil }
func (NopStore) HighWater(string, string) (uint64, error) { return 0, nil }
func (NopStore) List(Filter) ([]Event, error)             { return nil, nil }
func (NopStore) Streams(string) ([]StreamInfo, error)     { return nil, nil }
func (NopStore) DeleteBefore(time.Time) (int, error)      { return 0, nil }
