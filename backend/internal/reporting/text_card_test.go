package reporting

import (
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

func TestValidateTextCardMarkdownAcceptsSubset(t *testing.T) {
	accepted := []string{
		"# 标题\n\n普通段落与 **加粗**、*斜体*、`代码`。",
		"- 项目一\n- 项目二\n\n1. 有序项\n2. 第二项",
		"> 引用行\n\n[文档链接](https://example.com/guide)",
		"```go\nfmt.Println(\"hello\")\n```",
		"带 [链接](http://example.com/path?q=1) 的段落",
	}
	for _, content := range accepted {
		if e := ValidateTextCardMarkdown(content); e != nil {
			t.Fatalf("expected accepted content %q, got %v", content, e)
		}
	}
}

func TestValidateTextCardMarkdownRejectsUnsafeContent(t *testing.T) {
	rejected := []string{
		"",
		strings.Repeat("好", MaxTextCardContentRunes+1),
		`<script>alert(1)</script>`,
		`<iframe src="https://example.com"></iframe>`,
		`<!-- 注释 -->`,
		"![图片](https://example.com/a.png)",
		"[点击](javascript:alert(1))",
		"[点击](JaVaScRiPt:alert(1))",
		"[数据](data:text/html,<script>alert(1)</script>)",
		"[vb](vbscript:msgbox(1))",
		"[相对链接](/relative/path)",
		"未闭合代码块 ```go",
	}
	for _, content := range rejected {
		if e := ValidateTextCardMarkdown(content); e == nil {
			t.Fatalf("expected rejection for %q", content)
		}
	}
}

func TestNormalizeAcceptsTextCardWidget(t *testing.T) {
	def := DashboardDefinition{
		Title: "文本卡看板",
		Widgets: []DashboardWidget{
			{
				ID:           dashboardWidgetOne,
				Title:        "说明卡",
				ReadingOrder: 0,
				Layout:       DashboardLayout{X: 0, Y: 0, Width: 12, Height: 4},
				TextCard:     &TextCard{Content: "# 欢迎\n\n仅 **http/https** 链接：[文档](https://example.com)"},
			},
		},
	}
	normalized, e := NormalizeDashboardDefinition(def)
	if e != nil {
		t.Fatal(e)
	}
	if normalized.Widgets[0].TextCard == nil {
		t.Fatal("text card must survive normalization")
	}
}

func TestNormalizeRejectsTextCardViolations(t *testing.T) {
	base := func(widget DashboardWidget) DashboardDefinition {
		return DashboardDefinition{
			Title:   "文本卡看板",
			Widgets: []DashboardWidget{widget},
		}
	}
	layout := DashboardLayout{X: 0, Y: 0, Width: 12, Height: 4}
	cases := []DashboardDefinition{
		// Text card combined with an analysis source.
		base(DashboardWidget{
			ID: dashboardWidgetOne, Title: "混合", ReadingOrder: 0, Layout: layout,
			AnalysisID: dashboardAnalysisID, AnalysisVersion: 1,
			TextCard: &TextCard{Content: "正文"},
		}),
		// Text card carrying parameter bindings.
		base(DashboardWidget{
			ID: dashboardWidgetOne, Title: "绑定卡", ReadingOrder: 0, Layout: layout,
			TextCard: &TextCard{Content: "正文"},
			ParameterBindings: []ParameterBinding{
				{ParameterID: "p", Field: analysis.Field{Kind: "system", Key: "uid"}},
			},
		}),
		// Unsafe markup.
		base(DashboardWidget{
			ID: dashboardWidgetOne, Title: "危险卡", ReadingOrder: 0, Layout: layout,
			TextCard: &TextCard{Content: `<img src="x">`},
		}),
		// No source at all.
		base(DashboardWidget{ID: dashboardWidgetOne, Title: "空卡", ReadingOrder: 0, Layout: layout}),
	}
	for _, def := range cases {
		if _, e := NormalizeDashboardDefinition(def); e == nil {
			t.Fatalf("expected rejection for %+v", def.Widgets[0])
		}
	}
}
