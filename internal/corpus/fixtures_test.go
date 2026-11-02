//go:build cgo

package corpus

var fixtures = []fixture{goFixture, pythonFixture, tsFixture, phpFixture}

const goGates = `gates:
  parse:    {tool: ts.parse}
  scope:    {tool: diff.scope}
  nonempty: {tool: diff.nonempty}
  build:    {tool: go.build}
  vet:      {tool: go.vet}
  unit:     {tool: go.test, policy: baseline}
  api:      {tool: ts.api_contract}
order: [parse, scope, nonempty, build, vet, unit, api]
`

var goFixture = fixture{
	name:  "go",
	needs: []string{"go"},
	scope: []string{"calc/**"},
	base: map[string]string{
		".spine/gates.yaml": goGates,
		"go.mod":            "module ex.com/calc\n\ngo 1.22\n",
		"README.md":         "calc\n",
		"calc/calc.go":      "package calc\n\n// Add adds.\nfunc Add(a, b int) int { return a + b }\n\n// Sub subtracts.\nfunc Sub(a, b int) int { return a - b }\n",
		// Pre-existing debt: TestLegacy fails on base.
		"calc/calc_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"add\")\n\t}\n}\n\nfunc TestLegacy(t *testing.T) { t.Fatal(\"legacy rounding bug\") }\n",
	},
	tasks: []task{
		{name: "good", edit: map[string]string{
			"calc/mul.go":      "package calc\n\n// Mul multiplies.\nfunc Mul(a, b int) int { return a * b }\n",
			"calc/mul_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestMul(t *testing.T) {\n\tif Mul(2, 3) != 6 {\n\t\tt.Fatal(\"mul\")\n\t}\n}\n",
		}, pass: true, excused: map[string]int{"unit": 1}, fixed: map[string]int{"unit": 0}},
		{name: "fix-debt", edit: map[string]string{
			"calc/calc_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"add\")\n\t}\n}\n\nfunc TestLegacy(t *testing.T) {}\n",
		}, pass: true, fixed: map[string]int{"unit": 1}},
		{name: "new-test-failure", edit: map[string]string{
			"calc/mul.go":      "package calc\n\nfunc Mul(a, b int) int { return a + b }\n",
			"calc/mul_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestMul(t *testing.T) {\n\tif Mul(2, 3) != 6 {\n\t\tt.Fatalf(\"got %d\", Mul(2, 3))\n\t}\n}\n",
		}, failedGate: "unit", reason: "NewFailures", code: "TestMul", excused: map[string]int{"unit": 1}},
		{name: "syntax-error", edit: map[string]string{
			"calc/calc.go": "package calc\n\nfunc Add(a, b int) int { return a + b\n\nfunc Sub(a, b int) int { return a - b }\n",
		}, failedGate: "parse"},
		{name: "out-of-scope", edit: map[string]string{"README.md": "calc!\n", "calc/doc.go": "// Package calc.\npackage calc\n"},
			failedGate: "scope", code: "OutOfScope"},
		{name: "empty", failedGate: "nonempty", code: "NoChange"},
		{name: "type-error", edit: map[string]string{
			"calc/calc.go": "package calc\n\nfunc Add(a, b int) int { return \"x\" }\n\nfunc Sub(a, b int) int { return a - b }\n",
		}, failedGate: "build", code: "compile"},
		{name: "vet-error", edit: map[string]string{
			"calc/show.go": "package calc\n\nimport \"fmt\"\n\n// Show prints.\nfunc Show() string { return fmt.Sprintf(\"%d\", \"s\") }\n",
		}, failedGate: "vet", code: "compile"},
		{name: "api-removed", edit: map[string]string{
			"calc/calc.go": "package calc\n\n// Add adds.\nfunc Add(a, b int) int { return a + b }\n",
		}, failedGate: "api", code: "Removed"},
		{name: "api-changed", edit: map[string]string{
			"calc/calc.go": "package calc\n\n// Add adds.\nfunc Add(a, b int64) int64 { return a + b }\n\n// Sub subtracts.\nfunc Sub(a, b int) int { return a - b }\n",
		}, failedGate: "api", code: "SignatureChanged"},
		{name: "unresolved-import", edit: map[string]string{
			"calc/ghost.go": "package calc\n\nimport _ \"ex.com/calc/ghost\"\n",
		}, failedGate: "build"},
	},
}

const pyGates = `gates:
  parse:    {tool: ts.parse}
  scope:    {tool: diff.scope}
  nonempty: {tool: diff.nonempty}
  lint:     {tool: cmd.run, policy: baseline, args: {argv: [ruff, check, --output-format=json, --no-cache, --select, "F,E9", "."], parser: ruff-json}}
  unit:     {tool: cmd.run, policy: baseline, args: {argv: [pytest, -q, -p, no:cacheprovider, "--junitxml={tmp}/junit.xml"], report: "{tmp}/junit.xml", parser: pytest-junit, env: {PYTHONPATH: "."}}}
  api:      {tool: ts.api_contract}
order: [parse, scope, nonempty, lint, unit, api]
`

var pythonFixture = fixture{
	name:  "python",
	needs: []string{"ruff", "pytest"},
	scope: []string{"pkg/**", "tests/**"},
	base: map[string]string{
		".spine/gates.yaml": pyGates,
		"pkg/__init__.py":   "",
		"pkg/core.py":       "def area(r):\n    return 3.14159 * r * r\n\n\ndef _helper():\n    return 1\n",
		// Pre-existing debt: an unused import (F401) and a failing test.
		"pkg/legacy.py":      "import os\n\nVALUE = 1\n",
		"tests/__init__.py":  "",
		"tests/test_core.py": "from pkg.core import area\n\n\ndef test_area():\n    assert round(area(1), 2) == 3.14\n\n\ndef test_legacy():\n    assert False, 'legacy'\n",
	},
	tasks: []task{
		{name: "good", edit: map[string]string{
			"pkg/geo.py":        "def perimeter(r):\n    return 2 * 3.14159 * r\n",
			"tests/test_geo.py": "from pkg.geo import perimeter\n\n\ndef test_perimeter():\n    assert round(perimeter(1), 2) == 6.28\n",
		}, pass: true, excused: map[string]int{"lint": 1, "unit": 1}},
		{name: "fix-debt", edit: map[string]string{
			"pkg/legacy.py": "VALUE = 1\n",
		}, pass: true, fixed: map[string]int{"lint": 1, "unit": 0}, excused: map[string]int{"unit": 1}},
		{name: "new-lint-error", edit: map[string]string{
			"pkg/geo.py": "import sys\n\n\ndef perimeter(r):\n    return 2 * r\n",
		}, failedGate: "lint", reason: "NewFailures", code: "F401", excused: map[string]int{"lint": 1}},
		{name: "new-test-failure", edit: map[string]string{
			"tests/test_geo.py": "def test_wrong():\n    assert 1 == 2\n",
		}, failedGate: "unit", reason: "NewFailures", code: "test_wrong"},
		{name: "syntax-error", edit: map[string]string{"pkg/core.py": "def area(r:\n    return r\n"}, failedGate: "parse"},
		{name: "api-changed", edit: map[string]string{
			"pkg/core.py": "def area(radius, pi=3.14159):\n    return pi * radius * radius\n\n\ndef _helper():\n    return 1\n",
		}, failedGate: "api", code: "SignatureChanged"},
		{name: "private-change-ok", edit: map[string]string{
			"pkg/core.py": "def area(r):\n    return 3.14159 * r * r\n\n\ndef _helper2():\n    return 2\n",
		}, pass: true},
		{name: "unresolved-import", edit: map[string]string{
			"pkg/extra.py": "from .ghost import thing\n\nX = thing\n",
		}, failedGate: "api", code: "UnresolvedImport"},
	},
}

const tsGates = `gates:
  parse:    {tool: ts.parse}
  scope:    {tool: diff.scope}
  nonempty: {tool: diff.nonempty}
  types:    {tool: cmd.run, policy: baseline, args: {argv: [tsc, -p, ., --pretty, "false"], parser: tsc}}
  lint:     {tool: cmd.run, policy: baseline, args: {argv: [eslint, -f, json, scripts], parser: eslint-json}}
  api:      {tool: ts.api_contract}
order: [parse, scope, nonempty, types, lint, api]
`

var tsFixture = fixture{
	name:  "typescript",
	needs: []string{"tsc", "eslint"},
	scope: []string{"src/**", "scripts/**"},
	base: map[string]string{
		".spine/gates.yaml": tsGates,
		"package.json":      `{"name": "web", "private": true, "type": "module"}`,
		"tsconfig.json":     `{"compilerOptions": {"strict": true, "noEmit": true, "target": "es2020", "module": "esnext", "moduleResolution": "bundler"}, "include": ["src"]}`,
		"eslint.config.mjs": "export default [{ files: [\"scripts/**/*.js\"], rules: { \"no-unused-vars\": \"error\" } }];\n",
		"src/math.ts":       "export function add(a: number, b: number): number {\n  return a + b;\n}\n",
		// Pre-existing debt: a type error and a lint error.
		"src/legacy.ts":    "export const legacy: number = \"x\" as unknown as string;\n",
		"scripts/build.js": "const unused = 1;\nconsole.log('build');\n",
	},
	tasks: []task{
		{name: "good", edit: map[string]string{
			"src/mul.ts": "import { add } from './math';\n\nexport function double(a: number): number {\n  return add(a, a);\n}\n",
		}, pass: true, excused: map[string]int{"types": 1}},
		{name: "new-type-error", edit: map[string]string{
			"src/mul.ts": "export const bad: string = 1;\n",
		}, failedGate: "types", reason: "NewFailures", code: "TS2322", excused: map[string]int{"types": 1}},
		{name: "new-lint-error", edit: map[string]string{
			"scripts/build.js": "const unused = 1;\nconst another = 2;\nconsole.log('build');\n",
		}, failedGate: "lint", reason: "NewFailures", code: "no-unused-vars", excused: map[string]int{"lint": 1}},
		{name: "syntax-error", edit: map[string]string{"src/math.ts": "export function add(a: number, b: number: number {\n"}, failedGate: "parse"},
		{name: "api-removed", edit: map[string]string{"src/math.ts": "export function sum(a: number, b: number): number {\n  return a + b;\n}\n"},
			failedGate: "api", code: "Removed"},
		{name: "unresolved-import", edit: map[string]string{
			"scripts/deploy.js": "import lp from 'left-pad';\nconsole.log(lp('x', 3));\n",
		}, failedGate: "api", code: "UnresolvedImport"},
	},
}

const phpGates = `gates:
  parse:    {tool: ts.parse}
  scope:    {tool: diff.scope}
  nonempty: {tool: diff.nonempty}
  lint:     {tool: cmd.run, args: {argv: [sh, -c, "find src -name '*.php' | sort | xargs -n1 php -l"], parser: php-lint}}
  api:      {tool: ts.api_contract}
order: [parse, scope, nonempty, lint, api]
`

var phpFixture = fixture{
	name:  "php",
	needs: []string{"php"},
	scope: []string{"src/**"},
	base: map[string]string{
		".spine/gates.yaml":    phpGates,
		"composer.json":        `{"autoload": {"psr-4": {"App\\": "src/"}}}`,
		"src/Models/User.php":  "<?php\nnamespace App\\Models;\n\nclass User\n{\n    public function name(): string { return 'n'; }\n    private function secret(): string { return 's'; }\n}\n",
		"src/Support/Util.php": "<?php\nnamespace App\\Support;\n\nfunction slug(string $s): string { return strtolower($s); }\n",
	},
	tasks: []task{
		{name: "good", edit: map[string]string{
			"src/Models/User.php": "<?php\nnamespace App\\Models;\n\nclass User\n{\n    public function name(): string { return 'n'; }\n    public function email(): string { return 'e'; }\n    private function secret(): string { return 's'; }\n}\n",
		}, pass: true},
		{name: "private-removed-ok", edit: map[string]string{
			"src/Models/User.php": "<?php\nnamespace App\\Models;\n\nclass User\n{\n    public function name(): string { return 'n'; }\n}\n",
		}, pass: true},
		{name: "api-removed", edit: map[string]string{
			"src/Models/User.php": "<?php\nnamespace App\\Models;\n\nclass User\n{\n    private function secret(): string { return 's'; }\n}\n",
		}, failedGate: "api", code: "Removed"},
		{name: "api-changed", edit: map[string]string{
			"src/Support/Util.php": "<?php\nnamespace App\\Support;\n\nfunction slug(string $s, string $sep = '-'): string { return strtolower($s); }\n",
		}, failedGate: "api", code: "SignatureChanged"},
		{name: "syntax-error", edit: map[string]string{"src/Support/Util.php": "<?php\nfunction slug( {\n"}, failedGate: "parse"},
		{name: "compile-error", edit: map[string]string{
			"src/Support/Dup.php": "<?php\nnamespace App\\Support;\n\nfunction dup($a, $a) { return $a; }\n",
		}, failedGate: "lint", code: "php.fatal"},
	},
}
