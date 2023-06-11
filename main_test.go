package main

import (
	"strings"
	"testing"
)

func TestAggregateResponses(t *testing.T) {
	testCases := []struct {
		name      string
		input     []string
		wantPkg   string
		wantImps  string
		wantFuncs string
	}{
		{
			name: "multiple responses with different imports",
			input: []string{
				"package main\n\nimport \"fmt\"\n\nfunc HelloWorld() {\n\tfmt.Println(\"Hello, world!\")\n}\n",
				"package main\n\nimport \"math\"\n\nfunc SquareRoot(x float64) float64 {\n\treturn math.Sqrt(x)\n}\n",
			},
			wantPkg:   "package main\n",
			wantImps:  "import (\n\t\"fmt\"\n\t\"math\"\n)",
			wantFuncs: "func HelloWorld() {\n\tfmt.Println(\"Hello, world!\")\n}\n\n\n\n\nfunc SquareRoot(x float64) float64 {\n\treturn math.Sqrt(x)\n}",
		},
		{
			name: "multiple responses with common imports",
			input: []string{
				"package main\n\nimport \"fmt\"\n\nfunc HelloWorld() {\n\tfmt.Println(\"Hello, world!\")\n}\n",
				"package main\n\nimport \"fmt\"\n\nfunc PrintName(name string) {\n\tfmt.Println(name)\n}\n",
			},
			wantPkg:   "package main\n",
			wantImps:  "import (\n\t\"fmt\"\n)",
			wantFuncs: "func HelloWorld() {\n\tfmt.Println(\"Hello, world!\")\n}\n\n\n\n\nfunc PrintName(name string) {\n\tfmt.Println(name)\n}",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			output := AggregateResponses("whatever", tc.input)

			// Check package declaration
			if !strings.Contains(output, tc.wantPkg) {
				t.Errorf("expected package declaration %q, got %q", tc.wantPkg, output)
			}

			// Check imports
			if !strings.Contains(output, tc.wantImps) {
				t.Errorf("expected imports %q, got %q", tc.wantImps, output)
			}

			// Check function bodies
			if !strings.Contains(output, tc.wantFuncs) {
				t.Errorf("expected function bodies %q, got %q", tc.wantFuncs, output)
			}
		})
	}
}
