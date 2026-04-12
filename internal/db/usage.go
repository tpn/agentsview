package db

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/tidwall/gjson"
)

// UsageFilter controls the date range, agent, and timezone
// for daily usage aggregation queries.
type UsageFilter struct {
	From     string // YYYY-MM-DD, inclusive
	To       string // YYYY-MM-DD, inclusive
	Agent    string // "claude", "codex", or "" for all
	Timezone string // IANA timezone, "" for UTC
}

// location loads the timezone or returns the system local timezone.
func (f UsageFilter) location() *time.Location {
	if f.Timezone == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(f.Timezone)
	if err != nil {
		return time.Local
	}
	return loc
}

// DailyUsageEntry holds token counts and cost for one day.
type DailyUsageEntry struct {
	Date                string           `json:"date"`
	InputTokens         int              `json:"inputTokens"`
	OutputTokens        int              `json:"outputTokens"`
	CacheCreationTokens int              `json:"cacheCreationTokens"`
	CacheReadTokens     int              `json:"cacheReadTokens"`
	TotalCost           float64          `json:"totalCost"`
	ModelsUsed          []string         `json:"modelsUsed"`
	ModelBreakdowns     []ModelBreakdown `json:"modelBreakdowns,omitempty"`
}

// ModelBreakdown holds per-model token and cost breakdown.
type ModelBreakdown struct {
	ModelName           string  `json:"modelName"`
	InputTokens         int     `json:"inputTokens"`
	OutputTokens        int     `json:"outputTokens"`
	CacheCreationTokens int     `json:"cacheCreationTokens"`
	CacheReadTokens     int     `json:"cacheReadTokens"`
	Cost                float64 `json:"cost"`
}

// UsageTotals holds aggregate token and cost totals.
type UsageTotals struct {
	InputTokens         int     `json:"inputTokens"`
	OutputTokens        int     `json:"outputTokens"`
	CacheCreationTokens int     `json:"cacheCreationTokens"`
	CacheReadTokens     int     `json:"cacheReadTokens"`
	TotalCost           float64 `json:"totalCost"`
}

// DailyUsageResult wraps the daily entries and totals.
type DailyUsageResult struct {
	Daily  []DailyUsageEntry `json:"daily"`
	Totals UsageTotals       `json:"totals"`
}

// modelRates holds per-model pricing in rate-per-token form.
type modelRates struct {
	input         float64
	output        float64
	cacheCreation float64
	cacheRead     float64
}

// loadPricingMap reads the model_pricing table into a map for
// in-memory joins. This is much faster than a SQL LEFT JOIN
// on every row of the daily usage scan, since the pricing
// table is tiny (a few dozen rows) and lookups are O(1).
func (db *DB) loadPricingMap(
	ctx context.Context,
) (map[string]modelRates, error) {
	rows, err := db.getReader().QueryContext(ctx,
		`SELECT model_pattern,
			input_per_mtok, output_per_mtok,
			cache_creation_per_mtok, cache_read_per_mtok
		 FROM model_pricing`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]modelRates)
	for rows.Next() {
		var (
			pattern string
			rates   modelRates
		)
		if err := rows.Scan(
			&pattern,
			&rates.input, &rates.output,
			&rates.cacheCreation, &rates.cacheRead,
		); err != nil {
			return nil, err
		}
		out[pattern] = rates
	}
	return out, rows.Err()
}

// paddedUTCBound pads a UTC timestamp by hours to cover timezone
// offsets. Positive hours pad forward, negative pad backward.
func paddedUTCBound(ts string, hours int) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return t.Add(time.Duration(hours) * time.Hour).Format(time.RFC3339)
}

// GetDailyUsage returns token usage and cost aggregated by day.
// It scans messages with non-empty token_usage JSON blobs,
// parses them in Go (faster than SQLite's json_extract per row),
// joins against an in-memory pricing map, and buckets by
// local date.
func (db *DB) GetDailyUsage(
	ctx context.Context, f UsageFilter,
) (DailyUsageResult, error) {
	loc := f.location()

	pricing, err := db.loadPricingMap(ctx)
	if err != nil {
		return DailyUsageResult{},
			fmt.Errorf("loading pricing: %w", err)
	}

	query := `
SELECT
	COALESCE(m.timestamp, s.started_at) as ts,
	m.model,
	m.token_usage
FROM messages m
JOIN sessions s ON m.session_id = s.id
WHERE m.token_usage != ''
	AND m.model != ''
	AND m.model != '<synthetic>'
	AND s.deleted_at IS NULL`

	var args []any

	// Filter on message timestamp (not session started_at) so
	// long-lived sessions that span date boundaries are included.
	// Pad by ±14h to cover all timezone offsets — the actual
	// date filtering happens post-query via localDate.
	if f.From != "" {
		padded := paddedUTCBound(f.From+"T00:00:00Z", -14)
		query += " AND COALESCE(m.timestamp, s.started_at) >= ?"
		args = append(args, padded)
	}
	if f.To != "" {
		padded := paddedUTCBound(f.To+"T23:59:59Z", 14)
		query += " AND COALESCE(m.timestamp, s.started_at) <= ?"
		args = append(args, padded)
	}
	if f.Agent != "" {
		query += " AND s.agent = ?"
		args = append(args, f.Agent)
	}

	rows, err := db.getReader().QueryContext(ctx, query, args...)
	if err != nil {
		return DailyUsageResult{},
			fmt.Errorf("querying daily usage: %w", err)
	}
	defer rows.Close()

	// dateModel key for per-(date, model) accumulation
	type dateModelKey struct {
		date  string
		model string
	}
	type modelAccum struct {
		inputTok  int
		outputTok int
		cacheCr   int
		cacheRd   int
		cost      float64
	}

	accum := make(map[dateModelKey]*modelAccum)

	var (
		ts        string
		model     string
		tokenJSON string
	)
	for rows.Next() {
		if err := rows.Scan(&ts, &model, &tokenJSON); err != nil {
			return DailyUsageResult{},
				fmt.Errorf("scanning daily usage row: %w", err)
		}

		date := localDate(ts, loc)
		if f.From != "" && date < f.From {
			continue
		}
		if f.To != "" && date > f.To {
			continue
		}

		// token_usage is written by our parsers and never by
		// user input, so we trust it to be valid JSON. gjson
		// is permissive enough that a truncated-tail row still
		// yields its leading fields; a fully garbage row would
		// return zeros, but that path is not reachable from
		// any known parser. Skipping gjson.Valid here preserves
		// the hot-path speedup (O(n) per row → not free on a
		// 310k-row scan).
		usage := gjson.Parse(tokenJSON)
		inputTok := int(usage.Get("input_tokens").Int())
		outputTok := int(usage.Get("output_tokens").Int())
		cacheCrTok := int(usage.Get("cache_creation_input_tokens").Int())
		cacheRdTok := int(usage.Get("cache_read_input_tokens").Int())

		rates := pricing[model]
		cost := (float64(inputTok)*rates.input +
			float64(outputTok)*rates.output +
			float64(cacheCrTok)*rates.cacheCreation +
			float64(cacheRdTok)*rates.cacheRead) / 1_000_000

		key := dateModelKey{date: date, model: model}
		ma, ok := accum[key]
		if !ok {
			ma = &modelAccum{}
			accum[key] = ma
		}
		ma.inputTok += inputTok
		ma.outputTok += outputTok
		ma.cacheCr += cacheCrTok
		ma.cacheRd += cacheRdTok
		ma.cost += cost
	}
	if err := rows.Err(); err != nil {
		return DailyUsageResult{},
			fmt.Errorf("iterating daily usage rows: %w", err)
	}

	// Group by date
	type dayData struct {
		models map[string]*modelAccum
	}
	days := make(map[string]*dayData)
	for key, ma := range accum {
		dd, ok := days[key.date]
		if !ok {
			dd = &dayData{models: make(map[string]*modelAccum)}
			days[key.date] = dd
		}
		dd.models[key.model] = ma
	}

	// Sort dates ascending
	dateKeys := make([]string, 0, len(days))
	for d := range days {
		dateKeys = append(dateKeys, d)
	}
	sort.Strings(dateKeys)

	daily := make([]DailyUsageEntry, 0, len(dateKeys))
	var totals UsageTotals

	for _, date := range dateKeys {
		dd := days[date]

		var entry DailyUsageEntry
		entry.Date = date

		// Sort models by cost descending
		modelNames := make([]string, 0, len(dd.models))
		for m := range dd.models {
			modelNames = append(modelNames, m)
		}
		sort.Slice(modelNames, func(i, j int) bool {
			return dd.models[modelNames[i]].cost >
				dd.models[modelNames[j]].cost
		})

		entry.ModelsUsed = modelNames
		breakdowns := make(
			[]ModelBreakdown, 0, len(modelNames),
		)

		for _, model := range modelNames {
			ma := dd.models[model]
			entry.InputTokens += ma.inputTok
			entry.OutputTokens += ma.outputTok
			entry.CacheCreationTokens += ma.cacheCr
			entry.CacheReadTokens += ma.cacheRd
			entry.TotalCost += ma.cost

			breakdowns = append(breakdowns, ModelBreakdown{
				ModelName:           model,
				InputTokens:         ma.inputTok,
				OutputTokens:        ma.outputTok,
				CacheCreationTokens: ma.cacheCr,
				CacheReadTokens:     ma.cacheRd,
				Cost:                ma.cost,
			})
		}

		entry.ModelBreakdowns = breakdowns
		daily = append(daily, entry)

		totals.InputTokens += entry.InputTokens
		totals.OutputTokens += entry.OutputTokens
		totals.CacheCreationTokens += entry.CacheCreationTokens
		totals.CacheReadTokens += entry.CacheReadTokens
		totals.TotalCost += entry.TotalCost
	}

	if daily == nil {
		daily = []DailyUsageEntry{}
	}

	return DailyUsageResult{
		Daily:  daily,
		Totals: totals,
	}, nil
}
