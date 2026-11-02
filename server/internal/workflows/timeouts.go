package workflows

import "time"

// Model activities include search/page reads and a primary/fallback model chain.
// With both Qwen and DeepSeek limited to 180s, a 3-minute activity would cancel
// the primary before its timeout and leave no time for the fallback. Ten minutes
// allows the two model attempts plus the bounded search and page-read work.
const modelActivityTimeout = 10 * time.Minute
