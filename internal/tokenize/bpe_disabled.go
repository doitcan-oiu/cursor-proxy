//go:build notokenizer

package tokenize

// bpeCount 在 `-tags notokenizer` 构建下不编译分词器依赖，
// 一律回退到启发式估算，可省下约 11MB 二进制体积。
func bpeCount(_, _ string) (int, bool) { return 0, false }
