package pool

import (
	"testing"
)

// TestScreenerListenerStructure перевіряє що структури правильно визначені
func TestScreenerListenerStructure(t *testing.T) {
	// Перевірка що можна створити структури
	var stats PairStatistics
	stats.Pair = "BTCUSDT"
	stats.TotalSignals = 10

	if stats.Pair != "BTCUSDT" {
		t.Errorf("Expected BTCUSDT, got %s", stats.Pair)
	}

	if stats.TotalSignals != 10 {
		t.Errorf("Expected 10, got %d", stats.TotalSignals)
	}
}

// TestNewScreenerListener перевіряє створення listener
func TestNewScreenerListener(t *testing.T) {
	// Повинен повернути помилку для порожнього URL
	_, err := NewScreenerListener("", "key")
	if err == nil {
		t.Error("Expected error for empty URL")
	}

	// Повинен створитися для валідних параметрів
	listener, err := NewScreenerListener("https://test.supabase.co", "test-key")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if listener == nil {
		t.Error("Listener should not be nil")
	}

	if listener.supabaseURL != "https://test.supabase.co" {
		t.Errorf("Expected https://test.supabase.co, got %s", listener.supabaseURL)
	}
}
