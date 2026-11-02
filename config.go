package main

import (
	"math"
	"strconv"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

// settings sind die Plugin-Einstellungen (Navidrome-Plugin-Config, alles Strings).
type settings struct {
	DryRun       bool     // nur protokollieren, nichts schreiben (Standard: an)
	MinScore     float64  // Mindest-Ähnlichkeit Titel/Artist
	DurTol       float64  // erlaubte Laufzeit-Abweichung in Sekunden
	Limit        int      // max. Änderungen/Lookups pro Lauf (0 = unbegrenzt)
	Playlists    []string // nur Titel aus Wiedergabelisten mit diesen Namen (leer = keine Einschränkung)
	PlaylistUser string   // Navidrome-Benutzer, dessen Wiedergabelisten gelesen werden
	PathFilter   []string // nur Pfade, die einen dieser Teilstrings enthalten (leer = alle)
	RetryDays    float64  // abgelehnte Dateien erst nach X Tagen erneut versuchen
	MinAgeMin    float64  // Dateien jünger als X Minuten überspringen (Explo schreibt ggf. noch)
	Cron         string   // Zeitplan
	RunOnStart   bool     // kurz nach dem Laden einen Lauf starten
	BudgetMs     int      // Zeitbudget pro Plugin-Aufruf (hartes Limit von Navidrome: 30 s)
}

func cfgFloat(key string, def float64) float64 {
	if s, ok := host.ConfigGet(key); ok {
		if v, err := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(s, ",", ".")), 64); err == nil {
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				return v
			}
		}
	}
	return def
}

func cfgBool(key string, def bool) bool {
	if s, ok := host.ConfigGet(key); ok {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "1", "true", "yes", "ja", "an", "on":
			return true
		case "0", "false", "no", "nein", "aus", "off":
			return false
		}
	}
	return def
}

func loadSettings() settings {
	s := settings{
		DryRun:     cfgBool("dry_run", true),
		MinScore:   cfgFloat("min_score", 0.85),
		DurTol:     cfgFloat("dur_tol", 8),
		Limit:      int(cfgFloat("limit_per_run", 0)),
		RetryDays:  cfgFloat("retry_days", 7),
		MinAgeMin:  cfgFloat("min_age_min", 10),
		Cron:       defaultCron,
		RunOnStart: cfgBool("run_on_start", true),
		BudgetMs:   int(cfgFloat("budget_ms", 18000)),
	}
	if c, ok := host.ConfigGet("cron"); ok && strings.TrimSpace(c) != "" {
		s.Cron = strings.TrimSpace(c)
	}
	if f, ok := host.ConfigGet("path_filter"); ok {
		for _, p := range strings.Split(f, ",") {
			if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
				s.PathFilter = append(s.PathFilter, p)
			}
		}
	}
	if f, ok := host.ConfigGet("playlists"); ok {
		for _, p := range strings.Split(f, ",") {
			if p = strings.TrimSpace(p); p != "" {
				s.Playlists = append(s.Playlists, p)
			}
		}
	}
	if u, ok := host.ConfigGet("playlist_user"); ok {
		s.PlaylistUser = strings.TrimSpace(u)
	}
	// Unsinnige Werte auf sinnvolle Grenzen ziehen
	s.MinScore = min(max(s.MinScore, 0.5), 1)
	s.DurTol = max(s.DurTol, 0)
	s.Limit = max(s.Limit, 0)
	s.RetryDays = max(s.RetryDays, 0)
	s.MinAgeMin = max(s.MinAgeMin, 0)
	if s.BudgetMs < 2000 || s.BudgetMs > 24000 {
		s.BudgetMs = 18000
	}
	return s
}

func (s settings) allows(rel string) bool {
	if len(s.PathFilter) == 0 {
		return true
	}
	l := strings.ToLower(rel)
	for _, p := range s.PathFilter {
		if strings.Contains(l, p) {
			return true
		}
	}
	return false
}
