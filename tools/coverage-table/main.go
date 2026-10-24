// Command coverage-table renders the coverage table in README.md from a Go
// coverprofile.
//
// The table used to be hand-maintained, and it drifted the way hand-maintained
// numbers do: readings went stale, and a package that gained tests never
// appeared at all. Two rules stop that from recurring:
//
//   - Every package present in the profile must be claimed by exactly one row
//     in the row list below. A new provider that nobody gave a row fails the
//     generator, which prints the packages it could not place.
//   - The row list is the single source of truth for the table's shape,
//     including the notes, so regenerating is a pure function of (profile,
//     source tree) and there is no second place to edit.
//
// Usage:
//
//	make cover         # pinned Go, fresh profile, rewrite README.md in place
//	make cover-check   # same basis; exit 1 if README.md is stale
//
// The entrypoint measures cross-package (-coverpkg=./...) coverage. Rendering
// rounds statement counts half-up to one decimal. The script selects the exact
// toolchain in .github/coverage-go-version; the generated prose reads that same
// file.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const (
	startMarker = "<!-- coverage-table:start -->"
	endMarker   = "<!-- coverage-table:end -->"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "coverage-table:", err)
		os.Exit(1)
	}
}

func run() error {
	profilePath := flag.String("profile", "coverage.out", "coverprofile to read")
	readmePath := flag.String("readme", "README.md", "file holding the generated table")
	gomodPath := flag.String("gomod", "go.mod", "go.mod used to resolve the module path and root")
	check := flag.Bool("check", false, "verify the table is current instead of rewriting it")
	flag.Parse()

	modulePath, moduleRoot, err := readModule(*gomodPath)
	if err != nil {
		return err
	}
	version, err := readCoverageVersion(moduleRoot)
	if err != nil {
		return err
	}
	if got := runtime.Version(); got != "go"+version {
		return fmt.Errorf("coverage table requires Go %s (running %s); run `make cover` or `make cover-check`", version, got)
	}
	counts, err := readProfile(*profilePath, modulePath)
	if err != nil {
		return err
	}
	block, err := render(counts, moduleRoot)
	if err != nil {
		return err
	}

	current, err := os.ReadFile(*readmePath)
	if err != nil {
		return err
	}
	updated, err := replaceBlock(string(current), block)
	if err != nil {
		return fmt.Errorf("%s: %w", *readmePath, err)
	}

	if *check {
		if updated == string(current) {
			fmt.Printf("%s: coverage table is current\n", *readmePath)
			return nil
		}
		return fmt.Errorf("%s: coverage table is stale — run `make cover`\n%s", *readmePath, diffBlocks(string(current), block))
	}
	if updated == string(current) {
		fmt.Printf("%s: coverage table already current\n", *readmePath)
		return nil
	}
	if err := os.WriteFile(*readmePath, []byte(updated), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s: coverage table updated\n", *readmePath)
	return nil
}

// readModule returns the module path and the absolute directory holding go.mod.
func readModule(gomodPath string) (modulePath, moduleRoot string, err error) {
	data, err := os.ReadFile(gomodPath)
	if err != nil {
		return "", "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module ")
		if ok {
			modulePath = strings.TrimSpace(rest)
			break
		}
	}
	if modulePath == "" {
		return "", "", fmt.Errorf("%s: no module directive", gomodPath)
	}
	moduleRoot, err = filepath.Abs(filepath.Dir(gomodPath))
	if err != nil {
		return "", "", err
	}
	return modulePath, moduleRoot, nil
}

type stats struct {
	total   int
	covered int
}

// blockKey identifies one coverage block, so the profile can be de-duplicated.
//
// With -coverpkg every test binary instruments every package, so the same block
// is written once per binary, and the count in each copy describes what that
// one binary executed. The copies must therefore be merged, not deduplicated:
// keeping only the first occurrence scores a block as uncovered whenever the
// first binary to write it happened not to reach it, which drives most of the
// table to 0%.
type blockKey struct {
	file string
	pos  string
}

type blockState struct {
	pkg     string
	numStmt int
	covered bool
}

func readProfile(profilePath, modulePath string) (map[string]stats, error) {
	file, err := os.Open(profilePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)

	if !scanner.Scan() {
		return nil, fmt.Errorf("%s: empty profile", profilePath)
	}
	header := strings.TrimSpace(scanner.Text())
	mode, ok := strings.CutPrefix(header, "mode: ")
	if !ok {
		return nil, fmt.Errorf("%s: first line is %q, want a mode line", profilePath, header)
	}
	switch mode {
	case "set", "count", "atomic":
	default:
		return nil, fmt.Errorf("%s: unknown coverage mode %q", profilePath, mode)
	}

	blocks := make(map[blockKey]blockState)
	for line := 2; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		name, pos, numStmt, count, err := parseProfileLine(text)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", profilePath, line, err)
		}
		key := blockKey{file: name, pos: pos}
		state, exists := blocks[key]
		if !exists {
			pkg, err := packageOf(name, modulePath)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %w", profilePath, line, err)
			}
			blocks[key] = blockState{pkg: pkg, numStmt: numStmt, covered: count > 0}
			continue
		}
		// The same block must carry the same statement count in every copy:
		// the copies come from identical compiler output, so a disagreement
		// means the profile was assembled from different builds and the
		// denominator would be arbitrary. `go tool cover` rejects this too.
		if numStmt != state.numStmt {
			return nil, fmt.Errorf(
				"%s:%d: inconsistent NumStmt for %s: saw %d, an earlier sample had %d",
				profilePath, line, name, numStmt, state.numStmt)
		}
		if count > 0 && !state.covered {
			state.covered = true
			blocks[key] = state
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("%s: no coverage blocks", profilePath)
	}

	counts := make(map[string]stats)
	for _, state := range blocks {
		current := counts[state.pkg]
		current.total += state.numStmt
		if state.covered {
			current.covered += state.numStmt
		}
		counts[state.pkg] = current
	}
	return counts, nil
}

// parseProfileLine splits "<file>:<startLine>.<startCol>,<endLine>.<endCol>
// <numStmt> <count>". The file name is separated from the position by the last
// colon so a path containing one cannot confuse the split.
func parseProfileLine(text string) (file, pos string, numStmt, count int, err error) {
	fields := strings.Fields(text)
	if len(fields) != 3 {
		return "", "", 0, 0, fmt.Errorf("malformed profile line %q", text)
	}
	colon := strings.LastIndex(fields[0], ":")
	if colon < 0 {
		return "", "", 0, 0, fmt.Errorf("profile line %q carries no position", text)
	}
	file, pos = fields[0][:colon], fields[0][colon+1:]
	if numStmt, err = strconv.Atoi(fields[1]); err != nil {
		return "", "", 0, 0, fmt.Errorf("profile line %q: %w", text, err)
	}
	if count, err = strconv.Atoi(fields[2]); err != nil {
		return "", "", 0, 0, fmt.Errorf("profile line %q: %w", text, err)
	}
	if numStmt < 0 || count < 0 {
		return "", "", 0, 0, fmt.Errorf("profile line %q has a negative count", text)
	}
	return file, pos, numStmt, count, nil
}

// packageOf maps a profile file name to its import path relative to the module.
// The root package is "".
func packageOf(file, modulePath string) (string, error) {
	rel, ok := strings.CutPrefix(file, modulePath+"/")
	if !ok {
		if file == modulePath {
			return "", nil
		}
		return "", fmt.Errorf("file %q is outside module %q", file, modulePath)
	}
	pkg := path.Dir(rel)
	if pkg == "." {
		return "", nil
	}
	return pkg, nil
}

// percent rounds half-up to one decimal. The fixed rule makes the table a pure
// function of the profile. The function-level `go tool cover -func` total can
// use a different denominator: this table includes every profile block, even
// those outside function declarations.
//
// The rounding is done in integers on purpose. Scaling in float64 first loses
// the exact half at some boundaries: 1001/2000 is exactly 50.05%, but
// float64(1001)/float64(2000)*1000 comes out just under 500.5 and used to round
// down to 50.0 instead of half-up to 50.1.
func percent(covered, total int) float64 {
	if total <= 0 {
		return 0
	}
	// floor((covered*1000 + total/2) / total), with the division done once:
	// (2*covered*1000 + total) / (2*total) rounds the tenths half-up exactly.
	tenths := (2*int64(covered)*1000 + int64(total)) / (2 * int64(total))
	return float64(tenths) / 10
}

type rowKind int

const (
	// aggregate renders one percentage for the union of the packages it
	// claims. Claiming is first-match-wins in declaration order, so a specific
	// row must be declared before a broader one.
	aggregate rowKind = iota
	// spread renders "min–max%" across every package it matches and claims
	// nothing, so a summary line can sit above the rows it summarises.
	spread
)

type row struct {
	// label is the first column. A "%d" in it is replaced with the number of
	// packages the row matched, so a collapsed row can say how much it hides.
	label string
	note  string
	kind  rowKind
	match func(pkg string) bool
}

// isAdapter reports whether pkg is a provider adapter: a direct child of
// provider/ other than the shared compat library. compat is the base the
// compatible adapters build on (chat/stream readers, capability trimming,
// rerank dialects), not an adapter itself, so the adapter spread row must not
// include it — doing so would let the shared base widen the "adapters" interval.
func isAdapter(pkg string) bool {
	rest, ok := strings.CutPrefix(pkg, "provider/")
	return ok && !strings.Contains(rest, "/") && rest != "compat"
}

// adapterRows names the adapters that get a row of their own. Everything else
// under provider/ is either the thin aggregate or the spread summary, and a new
// adapter that arrives with its own tests lands in neither: the unclaimed-package
// check fails and asks for a row.
var adapterRows = []string{
	"provider/anthropic",
	"provider/dashscope",
	"provider/deepseek",
	"provider/easyrouter",
	"provider/gemini",
	"provider/minimax",
	"provider/moonshot",
	"provider/openai",
	"provider/openrouter",
	"provider/vercel",
	"provider/volcengine",
	"provider/zhipu",
}

func rows(thin map[string]bool) []row {
	exact := func(pkg string) func(string) bool {
		return func(candidate string) bool { return candidate == pkg }
	}
	// pkgRow is the common case: the label is the package path, monospaced.
	pkgRow := func(pkg, note string) row {
		return row{label: "`" + pkg + "`", note: note, match: exact(pkg)}
	}

	out := []row{
		{
			label: "门面层（根包）",
			match: exact(""),
		},
		pkgRow("internal/logging", ""),
		pkgRow("internal/safehttp", "SSRF 拨号拦截、大小上限、重定向降级、MIME 双重校验各有断言"),
		pkgRow("internal/httpx", ""),
		pkgRow("internal/sse", ""),
		pkgRow("internal/ipprivacy", ""),
		pkgRow("protocol/openaifiles", ""),
		pkgRow("protocol/openaibatch", "JSONL 编解码含 fuzz"),
		pkgRow("protocol/anthropic", ""),
		pkgRow("protocol/responses", ""),
		pkgRow("provider", "跨全部适配器共享的类型、流策略与守卫"),
		pkgRow("provider/compat", "OpenAI 兼容适配器的公共基座：chat/stream 读取、能力裁剪、rerank 方言"),
		{
			label: "`provider/*` 适配层（区间）",
			note:  "迁移自一个跑在生产上的网关，路径被真实流量验证过；这一行是全部适配器的跨度",
			kind:  spread,
			match: isAdapter,
		},
	}
	for _, pkg := range adapterRows {
		out = append(out, pkgRow(pkg, adapterRowNotes[pkg]))
	}
	out = append(out,
		row{
			label: "`provider/siliconflow` 等 %d 个薄封装",
			note:  "没有自有测试文件；跨包口径下由 `provider` 包的冒烟测试执行（这些包没有测试文件，包内口径不会单独统计）",
			match: func(pkg string) bool { return thin[pkg] },
		},
		pkgRow("cmd/llmkit-probe", "参数解析 / .env / 排版及探测逻辑有离线回归；真实厂商行为仍需 key 探测"),
		row{
			label: "`tools/*`（%d 个开发工具）",
			note:  "覆盖率表生成器及固定工具链入口有离线回归；开发工具同样计入分母",
			match: func(pkg string) bool { return pkg == "tools" || strings.HasPrefix(pkg, "tools/") },
		},
		row{
			label: "`examples/*`（%d 个示例程序）",
			note:  "示例只做编译与 `go vet` 检查，不跑真实请求；个别纯本地逻辑有单测，所以略高于 0% —— 它们是文档，不是被测代码，但计入 `-coverpkg=./...` 的分母",
			match: func(pkg string) bool { return pkg == "examples" || strings.HasPrefix(pkg, "examples/") },
		},
	)
	return out
}

// adapterRowNotes carries the handful of rows that need a word of context.
var adapterRowNotes = map[string]string{
	"provider/minimax":    "手写的 embeddings 翻译层有自己的测试；chat 路径仍走冒烟测试",
	"provider/openrouter": "媒体资产归一化（把 raw 条目替换成规范化条目）有专门的回归测试",
}

func render(counts map[string]stats, moduleRoot string) (string, error) {
	banner, err := coverageBanner(moduleRoot)
	if err != nil {
		return "", err
	}
	packages := make([]string, 0, len(counts))
	for pkg := range counts {
		packages = append(packages, pkg)
	}
	sort.Strings(packages)

	thin, err := thinAdapters(moduleRoot, packages)
	if err != nil {
		return "", err
	}

	claimed := make(map[string]bool)
	var body strings.Builder
	for _, current := range rows(thin) {
		var members []string
		for _, pkg := range packages {
			if !current.match(pkg) {
				continue
			}
			if current.kind == aggregate {
				if claimed[pkg] {
					continue
				}
				claimed[pkg] = true
			}
			members = append(members, pkg)
		}
		// Expand the placeholder even for an empty match, so the error below
		// names the row as it would appear instead of dumping a raw "%d".
		label := current.label
		if strings.Contains(label, "%d") {
			label = fmt.Sprintf(label, len(members))
		}
		if len(members) == 0 {
			return "", fmt.Errorf("row %q matches no package: delete it or re-scope it", label)
		}

		fmt.Fprintf(&body, "| %s | %s | %s |\n", label, value(members, counts, current.kind), current.note)
	}

	var unclaimed []string
	for _, pkg := range packages {
		if !claimed[pkg] {
			unclaimed = append(unclaimed, pkgDisplay(pkg))
		}
	}
	if len(unclaimed) > 0 {
		sort.Strings(unclaimed)
		return "", fmt.Errorf(
			"no row claims these packages, so the table would silently omit them: %s\n"+
				"add each to a row in the row list in tools/coverage-table/main.go",
			strings.Join(unclaimed, ", "))
	}

	grandTotal, grandCovered := 0, 0
	for _, current := range counts {
		grandTotal += current.total
		grandCovered += current.covered
	}

	var out strings.Builder
	out.WriteString(startMarker)
	out.WriteString("\n")
	out.WriteString(banner)
	out.WriteString("| 包 | 覆盖率 | 备注 |\n")
	out.WriteString("|---|---|---|\n")
	out.WriteString(body.String())
	out.WriteString("\n")
	fmt.Fprintf(&out, "**总覆盖率 %.1f%%**（%d/%d 条语句）。\n",
		percent(grandCovered, grandTotal), grandCovered, grandTotal)
	out.WriteString(endMarker)
	return out.String(), nil
}

func readCoverageVersion(moduleRoot string) (string, error) {
	versionFile := filepath.Join(moduleRoot, ".github", "coverage-go-version")
	data, err := os.ReadFile(versionFile)
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(data))
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(version) {
		return "", fmt.Errorf("%s: want an exact Go patch version, got %q", versionFile, version)
	}
	return version, nil
}

// The version and measurement instructions are generated with the numbers, so
// the table cannot silently describe a different measurement toolchain.
func coverageBanner(moduleRoot string) (string, error) {
	version, err := readCoverageVersion(moduleRoot)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`覆盖率现状。口径**跨包**，使用 **Go %s**；精确工具链版本由 `+"`.github/coverage-go-version`"+` 统一维护。Go 补丁版本也可能改变语句数统计，升级该文件后需重新生成下表。

跨包统计的是「某个包的语句有没有被**任何**测试执行到」，包内口径（`+"`go test ./... -coverprofile`"+`）只统计包自己的测试，两者数字不可混用：

`+"```bash"+`
make cover         # 固定工具链生成下表，保留 coverage.out 供 HTML 报告使用
make cover-check   # 同一口径重新测量，只校验下表是否过期
`+"```"+`

百分比按语句数四舍五入到一位小数，分母是 profile 里的**每一个语句块**。`+"`go tool cover -func`"+` 的 total 行只累计函数声明内的块（包级声明的块不计入），所以它的总数比这里略小、百分比也可能略有出入。

`, version), nil
}

// value renders the coverage cell for one row.
func value(members []string, counts map[string]stats, kind rowKind) string {
	if kind == aggregate {
		total, covered := 0, 0
		for _, pkg := range members {
			total += counts[pkg].total
			covered += counts[pkg].covered
		}
		if total == 0 {
			return "—"
		}
		return fmt.Sprintf("%.1f%%", percent(covered, total))
	}

	low, high := math.Inf(1), math.Inf(-1)
	for _, pkg := range members {
		current := counts[pkg]
		if current.total == 0 {
			continue
		}
		pct := percent(current.covered, current.total)
		low = math.Min(low, pct)
		high = math.Max(high, pct)
	}
	if math.IsInf(low, 1) {
		return "—"
	}
	return fmt.Sprintf("%.1f–%.1f%%", low, high)
}

// thinAdapters is the set of adapters with neither test files of their own nor
// a dedicated row; the table collapses them into one aggregate row. The rule is
// derived rather than listed: adding tests to one of them moves it out of the
// aggregate and into the unclaimed check, which is the prompt to give it a row.
// "Thin" therefore states exactly one thing here — no own test files — not that
// the adapter carries no logic; the row's note must not claim more than that.
func thinAdapters(moduleRoot string, packages []string) (map[string]bool, error) {
	thin := make(map[string]bool)
	for _, pkg := range packages {
		if !isAdapter(pkg) {
			continue
		}
		dir := filepath.Join(moduleRoot, filepath.FromSlash(pkg))
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", dir, err)
		}
		hasTests := false
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), "_test.go") {
				hasTests = true
				break
			}
		}
		// Only packages no dedicated row already covers may be collapsed.
		if !hasTests && !hasDedicatedRow(pkg) {
			thin[pkg] = true
		}
	}
	return thin, nil
}

func hasDedicatedRow(pkg string) bool {
	for _, name := range adapterRows {
		if name == pkg {
			return true
		}
	}
	return pkg == "provider/compat"
}

func pkgDisplay(pkg string) string {
	if pkg == "" {
		return "(root package)"
	}
	return pkg
}

// markerSpan returns the byte span from the start marker through the end
// marker, inclusive. The document must hold exactly one of each: a second pair
// would be stale text that neither --check nor --write could ever reach, so the
// README would keep asserting two different readings while looking current.
func markerSpan(document string) (start, end int, err error) {
	if count := strings.Count(document, startMarker); count != 1 {
		if count == 0 {
			return 0, 0, fmt.Errorf("missing %s", startMarker)
		}
		return 0, 0, fmt.Errorf("found %d %s markers, want exactly 1", count, startMarker)
	}
	if count := strings.Count(document, endMarker); count != 1 {
		if count == 0 {
			return 0, 0, fmt.Errorf("missing %s", endMarker)
		}
		return 0, 0, fmt.Errorf("found %d %s markers, want exactly 1", count, endMarker)
	}
	start = strings.Index(document, startMarker)
	end = strings.Index(document, endMarker)
	if end < start {
		return 0, 0, errors.New("the coverage-table markers are in the wrong order")
	}
	return start, end + len(endMarker), nil
}

// replaceBlock swaps the text between the two markers for block, keeping the
// markers themselves in place.
func replaceBlock(document, block string) (string, error) {
	start, end, err := markerSpan(document)
	if err != nil {
		return "", err
	}
	return document[:start] + block + document[end:], nil
}

// diffBlocks reports the lines that differ between the committed block and the
// regenerated one. The table's shape is stable and only the numbers move, so a
// line-by-line walk is enough to point at what changed.
func diffBlocks(committed, want string) string {
	current, err := blockOf(committed)
	if err != nil {
		return "current block: " + err.Error()
	}
	got := strings.Split(current, "\n")
	expected := strings.Split(want, "\n")

	var out strings.Builder
	out.WriteString("  line  committed -> regenerated\n")
	limit := max(len(got), len(expected))
	reported := 0
	for i := 0; i < limit; i++ {
		var a, b string
		if i < len(got) {
			a = got[i]
		}
		if i < len(expected) {
			b = expected[i]
		}
		if a == b {
			continue
		}
		fmt.Fprintf(&out, "  %4d  %s -> %s\n", i+1, orNone(a), orNone(b))
		reported++
		if reported == 20 {
			out.WriteString("  ... (truncated)\n")
			break
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

func blockOf(document string) (string, error) {
	start, end, err := markerSpan(document)
	if err != nil {
		return "", err
	}
	return document[start:end], nil
}

func orNone(line string) string {
	if line == "" {
		return "(absent)"
	}
	return line
}
