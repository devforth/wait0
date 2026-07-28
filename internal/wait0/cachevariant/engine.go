package cachevariant

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
	"github.com/expr-lang/expr/vm"
)

const HeaderName = "Cache-Variant"

type runtimeEnv struct {
	Header func(string, ...string) (string, error) `expr:"header"`
}

type ProgramSet struct {
	Sources     []string
	HeaderNames []string
	Fingerprint string
	programs    []*vm.Program
}

type Result struct {
	Values         []string
	RequestHeaders http.Header
}

type Engine struct {
	mu   sync.RWMutex
	sets map[string]*ProgramSet
}

func NewEngine() *Engine {
	return &Engine{sets: make(map[string]*ProgramSet)}
}

func Expressions(h http.Header) ([]string, error) {
	raw := h.Values(HeaderName)
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(raw))
	for i, value := range raw {
		source := strings.TrimSpace(value)
		if len(source) >= 2 && source[0] == '"' && source[len(source)-1] == '"' {
			unquoted, err := strconv.Unquote(source)
			if err != nil {
				return nil, fmt.Errorf("expression %d: invalid quoted value: %w", i+1, err)
			}
			source = unquoted
		}
		if strings.TrimSpace(source) == "" {
			return nil, fmt.Errorf("expression %d: must not be empty", i+1)
		}
		out = append(out, source)
	}
	return out, nil
}

func (e *Engine) Compile(sources []string) (*ProgramSet, error) {
	if len(sources) == 0 {
		return nil, nil
	}
	fingerprint := fingerprintSources(sources)
	e.mu.RLock()
	cached := e.sets[fingerprint]
	e.mu.RUnlock()
	if cached != nil {
		return cached, nil
	}

	headerNames := make(map[string]string)
	programs := make([]*vm.Program, 0, len(sources))
	for i, source := range sources {
		names, err := referencedHeaders(source)
		if err != nil {
			return nil, fmt.Errorf("expression %d: %w", i+1, err)
		}
		for _, name := range names {
			headerNames[strings.ToLower(name)] = http.CanonicalHeaderKey(name)
		}

		program, err := expr.Compile(
			source,
			expr.Env(runtimeEnv{}),
			expr.AsKind(reflect.String),
			expr.DisableBuiltin("now"),
			expr.MaxNodes(512),
		)
		if err != nil {
			return nil, fmt.Errorf("expression %d: %w", i+1, err)
		}
		programs = append(programs, program)
	}

	names := make([]string, 0, len(headerNames))
	for _, name := range headerNames {
		names = append(names, name)
	}
	sort.Strings(names)
	set := &ProgramSet{
		Sources:     append([]string(nil), sources...),
		HeaderNames: names,
		Fingerprint: fingerprint,
		programs:    programs,
	}

	e.mu.Lock()
	if existing := e.sets[fingerprint]; existing != nil {
		set = existing
	} else {
		e.sets[fingerprint] = set
	}
	e.mu.Unlock()
	return set, nil
}

func (e *Engine) Evaluate(set *ProgramSet, headers http.Header, host string) (Result, error) {
	if set == nil || len(set.programs) == 0 {
		return Result{}, nil
	}

	lookup := func(name string, fallback ...string) (string, error) {
		if len(fallback) > 1 {
			return "", fmt.Errorf("header(%q): accepts at most one fallback", name)
		}
		if strings.EqualFold(name, "Host") {
			if host != "" {
				return host, nil
			}
		} else if values, ok := headerValues(headers, name); ok {
			if len(values) == 0 {
				return "", nil
			}
			return values[0], nil
		}
		if len(fallback) == 1 {
			return fallback[0], nil
		}
		return "", fmt.Errorf("required request header %q is missing; use header(%q, <default>) to provide a default", name, name)
	}

	env := runtimeEnv{Header: lookup}
	values := make([]string, 0, len(set.programs))
	for i, program := range set.programs {
		value, err := expr.Run(program, env)
		if err != nil {
			return Result{}, fmt.Errorf("expression %d: %w", i+1, err)
		}
		s, ok := value.(string)
		if !ok {
			return Result{}, fmt.Errorf("expression %d: returned %T, want string", i+1, value)
		}
		values = append(values, s)
	}

	projected := make(http.Header, len(set.HeaderNames))
	for _, name := range set.HeaderNames {
		if strings.EqualFold(name, "Host") {
			if host != "" {
				projected.Set("Host", host)
			}
			continue
		}
		if values, ok := headerValues(headers, name); ok {
			projected[name] = append([]string(nil), values...)
		}
	}
	return Result{Values: values, RequestHeaders: projected}, nil
}

func fingerprintSources(sources []string) string {
	h := sha256.New()
	for _, source := range sources {
		fmt.Fprintf(h, "%d:", len(source))
		_, _ = h.Write([]byte(source))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func headerValues(headers http.Header, name string) ([]string, bool) {
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			return values, true
		}
	}
	return nil, false
}

type headerVisitor struct {
	names []string
	err   error
}

func (v *headerVisitor) Visit(node *ast.Node) {
	if v.err != nil {
		return
	}
	call, ok := (*node).(*ast.CallNode)
	if !ok {
		return
	}
	ident, ok := call.Callee.(*ast.IdentifierNode)
	if !ok || ident.Value != "header" {
		return
	}
	if len(call.Arguments) < 1 || len(call.Arguments) > 2 {
		v.err = fmt.Errorf("header() expects one or two arguments")
		return
	}
	name, ok := call.Arguments[0].(*ast.StringNode)
	if !ok || strings.TrimSpace(name.Value) == "" {
		v.err = fmt.Errorf("header() name must be a non-empty string literal")
		return
	}
	if len(call.Arguments) == 2 {
		if _, ok := call.Arguments[1].(*ast.StringNode); !ok {
			v.err = fmt.Errorf("header() fallback must be a string literal")
			return
		}
	}
	v.names = append(v.names, name.Value)
}

func referencedHeaders(source string) ([]string, error) {
	tree, err := parser.Parse(source)
	if err != nil {
		return nil, err
	}
	visitor := &headerVisitor{}
	ast.Walk(&tree.Node, visitor)
	if visitor.err != nil {
		return nil, visitor.err
	}
	return visitor.names, nil
}
