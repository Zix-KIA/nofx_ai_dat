# Screener Integration - Інтеграція зі скрінерами

## 📋 Огляд

Інтеграція для обробки REALTIME сигналів від торгових скрінерів (SC, TS, HAS ботів) через Supabase.

## 🎯 Архітектура

```
Скрінери (SC/TS/HAS боти)
    ↓ INSERT в signals
PostgreSQL TRIGGER
    ↓ UPDATE pair_statistics2
Supabase REST API
    ↓ HTTP Polling (кожні 5 сек)
NOFX ScreenerListener
    ↓ Нові сигнали через channel
AI Decision Engine
    ↓ Аналіз + контекст
AutoTrader
    ↓ Виконання торгів
```

## 📦 Зміни в коді

### 1. База даних (`config/database.go`)

**Таблиця `user_signal_sources` - додано поля:**
```sql
screener_enabled BOOLEAN DEFAULT 0     -- Чи увімкнено моніторинг
supabase_url TEXT DEFAULT ''           -- URL Supabase проекту
supabase_key TEXT DEFAULT ''           -- API ключ
```

**Таблиця `traders` - додано поле:**
```sql
use_screener_signals BOOLEAN DEFAULT 0 -- Чи використовує трейдер сигнали
```

**Нові методи:**
- `UpdateUserSignalSourceScreener()` - оновлення screener конфігурації
- `GetUserSignalSource()` - отримання конфігурації (розширено)

### 2. Screener Listener (`pool/screener_listener.go`)

**Нові структури даних:**

```go
// Статистика по парі з pair_statistics2
type PairStatistics struct {
    Pair                 string
    TotalSignals         int      // Загальна к-сть сигналів
    SCCount              int      // Сигнали SC
    TSBinanceCount       int      // Сигнали TS Binance
    TSBybitCount         int      // Сигнали TS Bybit
    HASBinanceCount      int      // Сигнали HAS Binance
    HASBybitCount        int      // Сигнали HAS Bybit
    SCLastColor          string   // Останній колір SC (🟦🟧🟥⬜)
    BTCCorrAvg           float64  // Середня кореляція з BTC
    LastSignalDatetime   time.Time
    UpdatedAt            time.Time
    // ... інші поля
}

// Подія нового сигналу
type ScreenerSignalEvent struct {
    Pair       string          // BTCUSDT
    Statistics *PairStatistics // Повна статистика
    Timestamp  time.Time       // Час події
}

// Listener для моніторингу
type ScreenerListener struct {
    supabaseURL  string
    supabaseKey  string
    httpClient   *http.Client
    statistics   map[string]*PairStatistics  // Кеш даних
    signalChan   chan *ScreenerSignalEvent   // Канал подій
    pollInterval time.Duration               // Інтервал опитування (5 сек)
    // ... контроль goroutines
}
```

**API методи:**

```go
// Створити listener
func NewScreenerListener(supabaseURL, supabaseKey string) (*ScreenerListener, error)

// Запустити моніторинг
func (sl *ScreenerListener) Start() error

// Зупинити моніторинг
func (sl *ScreenerListener) Stop()

// Отримати канал для нових сигналів
func (sl *ScreenerListener) GetSignalChannel() <-chan *ScreenerSignalEvent

// Отримати статистику по парі
func (sl *ScreenerListener) GetStatistics(pair string) (*PairStatistics, bool)

// Отримати топ N пар по сигналам
func (sl *ScreenerListener) GetTopSignalPairs(limit int) []string

// Глобальний listener
func InitGlobalScreenerListener(supabaseURL, supabaseKey string) error
func GetGlobalScreenerListener() *ScreenerListener
func StopGlobalScreenerListener()
```

**Як працює:**

1. **Початкове завантаження:**
   ```
   GET {supabase_url}/rest/v1/pair_statistics2?select=*&order=total_signals.desc
   Headers:
     apikey: {supabase_key}
     Authorization: Bearer {supabase_key}
   ```
   Завантажує всі пари та їх статистику

2. **Polling loop (кожні 5 сек):**
   ```
   GET {supabase_url}/rest/v1/pair_statistics2?select=*&updated_at=gte.{1_min_ago}&order=updated_at.desc
   ```
   Отримує тільки пари оновлені за останню хвилину

3. **Виявлення нових сигналів:**
   - Порівнює `TotalSignals` зі старим значенням
   - Якщо більше → новий сигнал!
   - Відправляє `ScreenerSignalEvent` в канал

4. **Логування:**
   ```
   🔔 новий сигнал: BTCUSDT (всього:16, SC:6, кореляція:0.44)
   ```

### 3. API Endpoints (`api/server.go`)

**GET `/api/user/signal-sources`**

Response:
```json
{
  "coin_pool_url": "https://...",
  "oi_top_url": "https://...",
  "screener_enabled": true,
  "supabase_url": "https://yourproject.supabase.co",
  "supabase_key": "eyJhbGc..."
}
```

**POST `/api/user/signal-sources`**

Request:
```json
{
  "coin_pool_url": "https://...",
  "oi_top_url": "https://...",
  "screener_enabled": true,
  "supabase_url": "https://yourproject.supabase.co",
  "supabase_key": "eyJhbGc..."
}
```

Response:
```json
{
  "message": "用户信号源配置已保存"
}
```

## 🚀 Використання

### 1. Налаштування через API

```bash
curl -X POST http://localhost:8080/api/user/signal-sources \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -d '{
    "screener_enabled": true,
    "supabase_url": "https://vqzqrxxxxxx.supabase.co",
    "supabase_key": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
  }'
```

### 2. Ініціалізація в коді (TODO)

В `main.go` додати:

```go
// Після створення database
signalSources, err := database.GetUserSignalSource("default")
if err == nil && signalSources.ScreenerEnabled {
    err = pool.InitGlobalScreenerListener(
        signalSources.SupabaseURL,
        signalSources.SupabaseKey,
    )
    if err != nil {
        log.Printf("⚠️  Screener listener помилка: %v", err)
    } else {
        log.Printf("✓ Screener Listener активний")
    }
}
```

### 3. Обробка сигналів в AutoTrader (TODO)

В `trader/auto_trader.go`:

```go
// В Start() методі
if config.UseScreenerSignals {
    go at.watchScreenerSignals()
}

// Новий метод
func (at *AutoTrader) watchScreenerSignals() {
    listener := pool.GetGlobalScreenerListener()
    if listener == nil {
        return
    }

    for event := range listener.GetSignalChannel() {
        log.Printf("🔔 Новий сигнал: %s (сигналів:%d, SC:%d, BTC corr:%.2f)",
            event.Pair,
            event.Statistics.TotalSignals,
            event.Statistics.SCCount,
            event.Statistics.BTCCorrAvg,
        )

        // Варіант 1: Додати до candidate coins для наступного циклу
        at.addCandidateCoin(event.Pair)

        // Варіант 2: Запустити позачерговий AI аналіз
        // at.analyzeSignal(event)
    }
}
```

### 4. Інтеграція з AI Decision (TODO)

В `decision/engine.go` додати screener контекст в промпт:

```go
func (e *Engine) buildPrompt(ctx *Context, pair string) string {
    // ... існуючий код

    // Додати screener статистику якщо доступна
    listener := pool.GetGlobalScreenerListener()
    if listener != nil {
        if stats, ok := listener.GetStatistics(pair); ok {
            prompt += fmt.Sprintf(`
Screener Signals:
- Total signals: %d (SC: %d, TS Binance: %d, TS Bybit: %d)
- Last SC color: %s
- BTC correlation: %.2f (30m:%.2f, 60m:%.2f, 120m:%.2f, 180m:%.2f)
- Last signal: %s

Consider the screener signals in your analysis.
`,
                stats.TotalSignals,
                stats.SCCount,
                stats.TSBinanceCount,
                stats.TSBybitCount,
                stats.SCLastColor,
                stats.BTCCorrAvg,
                stats.BTCCorr30,
                stats.BTCCorr60,
                stats.BTCCorr120,
                stats.BTCCorr180,
                stats.LastSignalDatetime.Format("15:04:05"),
            )
        }
    }

    return prompt
}
```

## 📊 Формат даних Supabase

### Таблиця `pair_statistics2`

```sql
CREATE TABLE pair_statistics2 (
    pair TEXT PRIMARY KEY,
    last_signal_datetime TIMESTAMP,
    sc_count INTEGER,
    ts_binance_count INTEGER,
    ts_bybit_count INTEGER,
    has_binance_count INTEGER,
    has_bybit_count INTEGER,
    total_signals INTEGER,
    sc_last_datetime TIMESTAMP,
    sc_last_color TEXT,
    btc_corr_30 NUMERIC,
    btc_corr_60 NUMERIC,
    btc_corr_120 NUMERIC,
    btc_corr_180 NUMERIC,
    btc_corr_avg NUMERIC,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);
```

**Приклад запису:**
```json
{
  "pair": "BTCUSDT",
  "total_signals": 16,
  "sc_count": 6,
  "ts_binance_count": 5,
  "ts_bybit_count": 3,
  "has_binance_count": 2,
  "has_bybit_count": 0,
  "sc_last_color": "🟦",
  "btc_corr_avg": 0.44,
  "btc_corr_30": 0.49,
  "btc_corr_60": 0.46,
  "btc_corr_120": 0.30,
  "btc_corr_180": 0.51,
  "last_signal_datetime": "2025-11-09T14:45:22.123456Z",
  "updated_at": "2025-11-09T14:45:22.456789Z"
}
```

## 🔧 Конфігурація

### Supabase URL
Формат: `https://yourproject.supabase.co`

### Supabase API Key
Отримати з Supabase Dashboard → Settings → API → anon/public key

### Інтервал polling
За замовчуванням: 5 секунд
Можна змінити в коді: `pollInterval: 5 * time.Second`

## ⚙️ Технічні деталі

### HTTP Headers для Supabase
```
apikey: {supabase_key}
Authorization: Bearer {supabase_key}
Accept: application/json
```

### Фільтр по часу (PostgREST)
```
updated_at=gte.2025-11-09T14:44:00Z
```
Формат: ISO 8601 (RFC3339)

### Сортування
```
order=total_signals.desc      // За кількістю сигналів
order=updated_at.desc          // За часом оновлення
```

## 🐛 Помилки та відладка

### Логи

**Успішний запуск:**
```
✓ Screener Listener连接成功到Supabase数据库
✓ 加载了45个交易对的历史数据
🎯 Screener Listener已启动 (轮询间隔: 5s)
👂 开始轮询pair_statistics2表的更新...
```

**Новий сигнал:**
```
🔔 новий сигнал: BTCUSDT (всього:16, SC:6, кореляція:0.44)
```

**Помилки:**
```
⚠️  HTTP请求失败: dial tcp: connection refused
⚠️  JSON解析失败: unexpected end of JSON input
❌ HTTP状态错误 401: {"message":"Invalid API key"}
```

### Troubleshooting

1. **401 Unauthorized**
   - Перевірити `supabase_key`
   - Переконатися що використовується anon/public key

2. **404 Not Found**
   - Перевірити URL: має бути `https://project.supabase.co`
   - Перевірити що таблиця `pair_statistics2` існує

3. **Немає сигналів**
   - Перевірити що боти працюють
   - Перевірити `updated_at` в БД
   - Збільшити вікно часу в фільтрі

## 📈 Метрики та статистика

Доступні методи:

```go
// Топ 20 пар по кількості сигналів
topPairs := listener.GetTopSignalPairs(20)

// Статистика по конкретній парі
if stats, ok := listener.GetStatistics("BTCUSDT"); ok {
    fmt.Printf("Total signals: %d\n", stats.TotalSignals)
    fmt.Printf("BTC correlation: %.2f\n", stats.BTCCorrAvg)
}

// Всі пари
allStats := listener.GetAllStatistics()
fmt.Printf("Всього пар: %d\n", len(allStats))
```

## 🎯 Наступні кроки

- [ ] Додати ініціалізацію в `main.go`
- [ ] Реалізувати обробку в `AutoTrader.watchScreenerSignals()`
- [ ] Інтегрувати з AI промптом в `decision/engine.go`
- [ ] Додати фільтри сигналів (мін. к-сть, колір, кореляція)
- [ ] Налаштувати WebSocket замість HTTP polling (опціонально)
- [ ] Додати метрики та dashboard для моніторингу

## 📝 Changelog

**2025-11-09** - Початкова реалізація
- Створено `ScreenerListener` з HTTP polling
- Додано поля в БД для Supabase конфігурації
- Оновлено API endpoints
- Замінено deprecated `ioutil` на `io`/`os`
