package main

import (
	"fmt"
	"log"
	"time"

	"nofx/pool"
)

func main() {
	// Supabase credentials
	supabaseURL := "https://dlrpanqtaxrwowaplzza.supabase.co"
	supabaseKey := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6ImRscnBhbnF0YXhyd293YXBsenphIiwicm9sZSI6ImFub24iLCJpYXQiOjE3NjAzMjk4OTUsImV4cCI6MjA3NTkwNTg5NX0.o3pf1_whm-2YajmmKdUabSI8G3sX5mNnuwmnwWTW2xo"

	log.Println("🚀 Testing ScreenerListener...")

	// Створити listener
	listener, err := pool.NewScreenerListener(supabaseURL, supabaseKey)
	if err != nil {
		log.Fatalf("❌ Failed to create listener: %v", err)
	}

	// Запустити listener
	if err := listener.Start(); err != nil {
		log.Fatalf("❌ Failed to start listener: %v", err)
	}

	log.Println("✅ ScreenerListener started successfully!")

	// Отримати топ 10 пар
	topPairs := listener.GetTopSignalPairs(10)
	log.Printf("\n📊 Top 10 pairs by total signals:")
	for i, pair := range topPairs {
		if stats, ok := listener.GetStatistics(pair); ok {
			log.Printf("  %d. %s: %d signals (SC:%d, TS_Binance:%d, TS_Bybit:%d, BTC_corr:%.2f, Color:%s)",
				i+1,
				pair,
				stats.TotalSignals,
				stats.SCCount,
				stats.TSBinanceCount,
				stats.TSBybitCount,
				stats.BTCCorrAvg,
				stats.SCLastColor,
			)
		}
	}

	// Слухати нові сигнали 30 секунд
	log.Printf("\n👂 Listening for new signals (30 seconds)...\n")

	signalChan := listener.GetSignalChannel()
	timeout := time.After(30 * time.Second)

	signalCount := 0

loop:
	for {
		select {
		case event := <-signalChan:
			signalCount++
			log.Printf("🔔 NEW SIGNAL #%d: %s", signalCount, event.Pair)
			log.Printf("   Total: %d, SC: %d, TS_Binance: %d, TS_Bybit: %d",
				event.Statistics.TotalSignals,
				event.Statistics.SCCount,
				event.Statistics.TSBinanceCount,
				event.Statistics.TSBybitCount,
			)
			log.Printf("   BTC Correlation: %.2f (30m:%.2f, 60m:%.2f, 120m:%.2f, 180m:%.2f)",
				event.Statistics.BTCCorrAvg,
				event.Statistics.BTCCorr30,
				event.Statistics.BTCCorr60,
				event.Statistics.BTCCorr120,
				event.Statistics.BTCCorr180,
			)
			log.Printf("   Last Signal: %s, Color: %s\n",
				event.Statistics.LastSignalDatetime.Format("15:04:05"),
				event.Statistics.SCLastColor,
			)

		case <-timeout:
			break loop
		}
	}

	if signalCount == 0 {
		log.Println("⏸️  No new signals received in 30 seconds (this is normal if no bots are active)")
	} else {
		log.Printf("✅ Received %d new signals!", signalCount)
	}

	// Зупинити listener
	listener.Stop()

	log.Println("✅ Test completed successfully!")
}
