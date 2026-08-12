package tools

import (
	"path/filepath"
	"strings"
)

// 上游写文件时用的扩展名 → 代码块语言标记。
// 命中不了也没关系，只是少一个语法高亮提示，不影响内容。
var extLanguage = map[string]string{
	".svg": "xml", ".xml": "xml", ".html": "html", ".htm": "html",
	".css": "css", ".scss": "scss", ".less": "less",
	".js": "javascript", ".mjs": "javascript", ".cjs": "javascript",
	".ts": "typescript", ".tsx": "tsx", ".jsx": "jsx", ".vue": "vue",
	".py": "python", ".go": "go", ".rs": "rust", ".java": "java",
	".kt": "kotlin", ".swift": "swift", ".dart": "dart", ".scala": "scala",
	".c": "c", ".h": "c", ".cpp": "cpp", ".cc": "cpp", ".hpp": "cpp",
	".cs": "csharp", ".rb": "ruby", ".php": "php", ".pl": "perl",
	".lua": "lua", ".r": "r", ".m": "objectivec", ".ex": "elixir",
	".sh": "bash", ".bash": "bash", ".zsh": "bash", ".fish": "fish",
	".ps1": "powershell", ".bat": "bat", ".sql": "sql", ".graphql": "graphql",
	".json": "json", ".jsonc": "json", ".yaml": "yaml", ".yml": "yaml",
	".toml": "toml", ".ini": "ini", ".conf": "ini", ".env": "bash",
	".proto": "protobuf", ".tf": "hcl", ".dockerfile": "dockerfile",
	".mk": "makefile", ".gradle": "groovy", ".csv": "csv", ".tex": "latex",
}

// proseExt 是「内容本身就是给人读的文章」的扩展名。
//
// 这类内容不能套代码块：用户让模型写 README、写方案、写文案时，
// 套上围栏会让客户端把整篇渲染成灰底源码，反而不如直接输出。
var proseExt = map[string]bool{
	"": true, ".md": true, ".markdown": true, ".mdx": true,
	".txt": true, ".text": true, ".rst": true, ".adoc": true, ".log": true,
}

// fenceFor 给一段将要当作正文输出的文件内容挑选代码块围栏。
//
// content 可以为空（流式场景下开头还看不到全文），此时退化为三个反引号；
// 已知全文时会按内容里最长的反引号串加长围栏，避免内容里自带的 ``` 提前收尾。
func fenceFor(path, content string) (open, close string) {
	ext := strings.ToLower(filepath.Ext(path))
	if proseExt[ext] {
		return "", ""
	}
	ticks := strings.Repeat("`", maxTickRun(content)+1)
	return ticks + extLanguage[ext] + "\n", ticks
}

// maxTickRun 返回内容里最长的连续反引号数，下限 2（保证围栏至少三个）。
func maxTickRun(s string) int {
	best, cur := 2, 0
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			cur++
			if cur > best {
				best = cur
			}
			continue
		}
		cur = 0
	}
	return best
}

// RenderContent 把一次「写文件」的内容还原成给纯对话客户端看的正文。
//
// 上游始终是 agent 形态：即便对话里没有声明任何工具，让它「写一段 SVG」「写个脚本」
// 「起草一份说明」，它都会把内容塞进一次写文件调用而不是直接回答。对纯聊天客户端
// 来说那份内容就是答案，这里按文件类型决定套不套代码块后原样给出。
func RenderContent(path, content string) string {
	if content == "" {
		return ""
	}
	open, close := fenceFor(path, content)
	if open == "" {
		return content
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return open + content + close
}
