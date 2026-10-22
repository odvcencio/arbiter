// Package main implements the arbiter CLI tool.
//
// Commands:
//
//	arbiter check <file.arb>                  — validate without emitting
//	arbiter compile <file.arb>                — compile to bytecode, print stats
//	arbiter eval <file.arb> --data '{...}'    — compile and eval against JSON
//	arbiter strategy <file.arb> [--name Name] --data '{...}' — evaluate one strategy
//	arbiter diff <base.arb> <candidate.arb> [--data '{...}' | --data-file contexts.json] [--key path] [--json] — compare governed outcomes
//	arbiter replay <rules.arb> --audit decisions.jsonl [--request-id id] [--limit N] [--json] — replay audited rule decisions
//	arbiter expert <file.arb> --envelope '{...}' [--facts '[...]'] — run one expert session
//	arbiter test [file.test.arb] [--verbose] — test rules, flags, and scenarios against expected outcomes
//	arbiter fmt <file.arb> [--check]           — format .arb files (--check for CI, exits 1 if unformatted)
//	arbiter bundle <file.arb> [-o output.arbb] [--risk-policy policy.arb] [--force] [--sign key.pem] — export obfuscated binary bundle for edge/browser (policy-gated)
//	arbiter bundle --verify <file.arbb> --pub <public-key.pem> — verify a signed bundle
//	arbiter import <file.json> [-o output.arb] — decompile Arishem JSON to .arb
//	arbiter runtime-capabilities <target> [--json] — inspect one runtime's gRPC capability surface
//	arbiter runtime-status <target> [--json] [--fail-on-issues] — inspect one runtime's status surface over gRPC
//	arbiter agent-status <target> [--json] [--fail-on-issues] — inspect one agent's status surface over gRPC
//	arbiter control-status <target> [--json] [--fail-on-issues] — inspect one hosted control plane's status surface over gRPC
//	arbiter status-issues [target] [--surface runtime|agent|control] [--json] — inspect the canonical status-issue vocabulary locally or from a live surface
//	arbiter serve [--grpc :8081] [--status 127.0.0.1:8082] [--audit-file decisions.jsonl] [--bundle-file bundles.json] [--overrides-file overrides.json] — start gRPC API
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
	"m31labs.dev/arbiter"
	arbiterv1 "m31labs.dev/arbiter/api/arbiter/v1"
	"m31labs.dev/arbiter/arbtest"
	"m31labs.dev/arbiter/audit"
	"m31labs.dev/arbiter/bundle"
	"m31labs.dev/arbiter/compiler"
	"m31labs.dev/arbiter/decompile"
	"m31labs.dev/arbiter/expert"
	explorepkg "m31labs.dev/arbiter/explore"
	"m31labs.dev/arbiter/flags"
	"m31labs.dev/arbiter/format"
	"m31labs.dev/arbiter/gostruct"
	"m31labs.dev/arbiter/grpcserver"
	"m31labs.dev/arbiter/internal/buildinfo"
	"m31labs.dev/arbiter/internal/grpcutil"
	"m31labs.dev/arbiter/internal/statusview"
	"m31labs.dev/arbiter/ir"
	"m31labs.dev/arbiter/observability"
	"m31labs.dev/arbiter/overrides"
	"m31labs.dev/arbiter/protoschema"
	"m31labs.dev/arbiter/vm"
)

const (
	commandList = "init, check, compile, eval, repl, fmt, strategy, diff, replay, expert, test, explore, import, runtime-capabilities, runtime-status, agent-status, control-status, status-issues, serve, completion"
	rootUsage   = "Usage: arbiter <command> <file>\nCommands: " + commandList
)

type usageError string

func (e usageError) Error() string { return string(e) }

var commandHandlers = map[string]func([]string) error{
	"init":                 runInit,
	"check":                runCheck,
	"compile":              runCompile,
	"fmt":                  runFmt,
	"bundle":               runBundle,
	"eval":                 runEval,
	"repl":                 runRepl,
	"strategy":             runStrategy,
	"diff":                 runDiff,
	"replay":               runReplay,
	"expert":               runExpert,
	"test":                 runTest,
	"explore":              runExplore,
	"import":               runImport,
	"runtime-capabilities": runRuntimeCapabilities,
	"runtime-status":       runRuntimeStatus,
	"agent-status":         runAgentStatus,
	"control-status":       runControlStatus,
	"status-issues":        runStatusIssues,
	"serve":                runServe,
	"completion":           runCompletion,
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		var usage usageError
		if errors.As(err, &usage) {
			fmt.Fprintln(os.Stderr, usage.Error())
		} else {
			fmt.Fprintln(os.Stderr, formatCLIError(err))
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError(rootUsage)
	}
	handler, ok := commandHandlers[args[0]]
	if !ok {
		return usageError(fmt.Sprintf("Unknown command: %s\nCommands: %s", args[0], commandList))
	}
	return handler(args[1:])
}

func runCheck(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter check <file.arb> [--strict] [--numeric-profile integer] [--proto set.binpb --message pkg.Message]")
	}
	strict := false
	numericProfile := ""
	for i := 1; i < len(args); i++ {
		name, value, hasValue := strings.Cut(args[i], "=")
		switch name {
		case "--strict":
			strict = true
		case "--numeric-profile":
			if !hasValue {
				if i+1 >= len(args) {
					return usageError("--numeric-profile requires a value (integer)")
				}
				value = args[i+1]
				i++
			}
			numericProfile = value
		case "--proto", "--message", "--go", "--type":
			// Parsed by schemaOptions; skip the separate value of "--flag value".
			if !hasValue {
				i++
			}
		default:
			if strings.HasPrefix(args[i], "--") {
				return usageError(fmt.Sprintf("unknown flag %q for check", args[i]))
			}
		}
	}
	if numericProfile != "" && numericProfile != "integer" {
		return usageError(fmt.Sprintf("unknown numeric profile %q (supported: integer)", numericProfile))
	}
	opts, err := schemaOptions(args[1:])
	if err != nil {
		return err
	}
	return checkWithProfile(args[0], strict, numericProfile, opts...)
}

func runCompile(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter compile <file.arb>")
	}
	return compileCmd(args[0])
}

func runEval(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter eval <file.arb> --data '{...}' [--proto set.binpb --message pkg.Message]")
	}
	dataJSON := ""
	explain := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--data":
			if i+1 < len(args) {
				dataJSON = args[i+1]
				i++
			}
		case "--explain":
			explain = true
		}
	}
	if dataJSON == "" {
		return fmt.Errorf("--data flag is required")
	}
	opts, err := schemaOptions(args[1:])
	if err != nil {
		return err
	}
	return evalCmd(args[0], dataJSON, explain, opts...)
}

// schemaOptions parses external-schema binding flags and returns a compile
// option that type-checks .arb field references against that schema, with zero
// duplication in the .arb source:
//
//	--proto <user.proto|set.binpb> --message <pkg.Message>
//	--go    <types.go>             --type    <StructName>
func schemaOptions(args []string) ([]arbiter.Option, error) {
	var protoPath, message, goPath, goType string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--proto":
			if i+1 < len(args) {
				protoPath = args[i+1]
				i++
			}
		case "--message":
			if i+1 < len(args) {
				message = args[i+1]
				i++
			}
		case "--go":
			if i+1 < len(args) {
				goPath = args[i+1]
				i++
			}
		case "--type":
			if i+1 < len(args) {
				goType = args[i+1]
				i++
			}
		}
	}

	var schema *ir.InputSchema
	var err error
	switch {
	case protoPath == "" && message == "" && goPath == "" && goType == "":
		return nil, nil
	case protoPath != "" || message != "":
		if protoPath == "" || message == "" {
			return nil, fmt.Errorf("--proto and --message must be used together")
		}
		if goPath != "" || goType != "" {
			return nil, fmt.Errorf("--proto and --go are mutually exclusive")
		}
		if strings.HasSuffix(protoPath, ".proto") {
			// Raw .proto source — parsed in-process (no protoc toolchain needed).
			schema, err = protoschema.FromProtoFile(protoPath, message)
		} else {
			// Compiled FileDescriptorSet (protoc/buf --descriptor_set_out / -o).
			var data []byte
			data, err = os.ReadFile(protoPath)
			if err != nil {
				return nil, fmt.Errorf("read descriptor set %s: %w", protoPath, err)
			}
			schema, err = protoschema.FromFileDescriptorSet(data, message)
		}
	default: // --go / --type
		if goPath == "" || goType == "" {
			return nil, fmt.Errorf("--go and --type must be used together")
		}
		schema, err = gostruct.FromStructFile(goPath, goType)
	}
	if err != nil {
		return nil, err
	}
	return []arbiter.Option{arbiter.WithInputSchema(schema)}, nil
}

func runStrategy(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter strategy <file.arb> [--name Name] --data '{...}'")
	}
	name := ""
	dataJSON := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--name":
			if i+1 < len(args) {
				name = args[i+1]
				i++
			}
		case "--data":
			if i+1 < len(args) {
				dataJSON = args[i+1]
				i++
			}
		}
	}
	if dataJSON == "" {
		return fmt.Errorf("--data flag is required")
	}
	return strategyCmd(args[0], name, dataJSON)
}

func runDiff(args []string) error {
	if len(args) < 2 {
		return usageError("Usage: arbiter diff <base.arb> <candidate.arb> [--data '{...}' | --data-file contexts.json] [--key path] [--json]")
	}
	dataJSON := ""
	dataFile := ""
	keyPath := ""
	jsonOut := false
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--data":
			if i+1 < len(args) {
				dataJSON = args[i+1]
				i++
			}
		case "--data-file":
			if i+1 < len(args) {
				dataFile = args[i+1]
				i++
			}
		case "--key":
			if i+1 < len(args) {
				keyPath = args[i+1]
				i++
			}
		case "--json":
			jsonOut = true
		}
	}
	return diffCmd(args[0], args[1], dataJSON, dataFile, keyPath, jsonOut)
}

func runReplay(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter replay <rules.arb> --audit decisions.jsonl [--request-id id] [--limit N] [--json]")
	}
	auditPath := ""
	requestID := ""
	limit := 0
	jsonOut := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--audit":
			if i+1 < len(args) {
				auditPath = args[i+1]
				i++
			}
		case "--request-id":
			if i+1 < len(args) {
				requestID = args[i+1]
				i++
			}
		case "--limit":
			if i+1 < len(args) {
				value, err := strconv.Atoi(args[i+1])
				if err != nil {
					return fmt.Errorf("invalid --limit %q: %w", args[i+1], err)
				}
				limit = value
				i++
			}
		case "--json":
			jsonOut = true
		}
	}
	return replayCmd(args[0], auditPath, requestID, limit, jsonOut)
}

func runExpert(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter expert <file.arb> --envelope '{...}' [--facts '[...]']")
	}
	envelopeJSON := ""
	factsJSON := ""
	for i := 1; i < len(args); i++ {
		if args[i] == "--envelope" && i+1 < len(args) {
			envelopeJSON = args[i+1]
			i++
		}
		if args[i] == "--facts" && i+1 < len(args) {
			factsJSON = args[i+1]
			i++
		}
	}
	if envelopeJSON == "" {
		return fmt.Errorf("--envelope flag is required")
	}
	return expertCmd(args[0], envelopeJSON, factsJSON)
}

func runTest(args []string) error {
	path := ""
	verbose := false
	coverage := false
	threshold := -1.0
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--verbose":
			verbose = true
		case "--coverage":
			coverage = true
		case "--threshold":
			coverage = true
			if i+1 >= len(args) {
				return usageError("--threshold requires a number between 0 and 100")
			}
			v, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil {
				return usageError("--threshold requires a number between 0 and 100")
			}
			threshold = v
			i++
		default:
			if !strings.HasPrefix(args[i], "-") && path == "" {
				path = args[i]
				continue
			}
			return usageError("Usage: arbiter test [file.test.arb] [--verbose] [--coverage] [--threshold N]\n\nWrite a .test.arb file next to your .arb bundle to test rules, flags, and scenarios against expected outcomes.")
		}
	}
	return testCmd(path, verbose, coverage, threshold)
}

func runImport(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter import <file.json> [-o output.arb]")
	}
	outPath := ""
	for i := 1; i < len(args); i++ {
		if args[i] == "-o" && i+1 < len(args) {
			outPath = args[i+1]
			i++
		}
	}
	return importCmd(args[0], outPath)
}

func runRuntimeCapabilities(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter runtime-capabilities <target> [--json] [--fail-on-contract-mismatch] [--token <token>] [--ca-file <pem>] [--server-name <name>] [--plaintext]")
	}
	cfg := parseRemoteInspectConfig(args)
	return runtimeCapabilitiesCmd(cfg)
}

func runRuntimeStatus(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter runtime-status <target> [--json] [--fail-on-issues] [--fail-on-contract-mismatch] [--token <token>] [--ca-file <pem>] [--server-name <name>] [--plaintext]")
	}
	return runtimeStatusCmd(parseRemoteInspectConfig(args))
}

func runAgentStatus(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter agent-status <target> [--json] [--fail-on-issues] [--fail-on-contract-mismatch] [--token <token>] [--ca-file <pem>] [--server-name <name>] [--plaintext]")
	}
	return agentStatusCmd(parseRemoteInspectConfig(args))
}

func runControlStatus(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter control-status <target> [--json] [--fail-on-issues] [--fail-on-contract-mismatch] [--token <token>] [--ca-file <pem>] [--server-name <name>] [--plaintext]")
	}
	return controlStatusCmd(parseRemoteInspectConfig(args))
}

func runStatusIssues(args []string) error {
	cfg := remoteInspectConfig{}
	surface := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			cfg.jsonOut = true
		case "--fail-on-contract-mismatch":
			cfg.failOnContractMismatch = true
		case "--surface":
			if i+1 >= len(args) {
				return usageError("Usage: arbiter status-issues [target] [--surface runtime|agent|control] [--json] [--fail-on-contract-mismatch] [--token <token>] [--ca-file <pem>] [--server-name <name>] [--plaintext]")
			}
			surface = strings.TrimSpace(args[i+1])
			i++
		case "--token":
			if i+1 >= len(args) {
				return usageError("Usage: arbiter status-issues [target] [--surface runtime|agent|control] [--json] [--fail-on-contract-mismatch] [--token <token>] [--ca-file <pem>] [--server-name <name>] [--plaintext]")
			}
			cfg.token = args[i+1]
			i++
		case "--ca-file":
			if i+1 >= len(args) {
				return usageError("Usage: arbiter status-issues [target] [--surface runtime|agent|control] [--json] [--fail-on-contract-mismatch] [--token <token>] [--ca-file <pem>] [--server-name <name>] [--plaintext]")
			}
			cfg.caFile = args[i+1]
			i++
		case "--server-name":
			if i+1 >= len(args) {
				return usageError("Usage: arbiter status-issues [target] [--surface runtime|agent|control] [--json] [--fail-on-contract-mismatch] [--token <token>] [--ca-file <pem>] [--server-name <name>] [--plaintext]")
			}
			cfg.serverName = args[i+1]
			i++
		case "--plaintext":
			cfg.forceInsecure = true
		default:
			if strings.HasPrefix(args[i], "--") {
				return usageError("Usage: arbiter status-issues [target] [--surface runtime|agent|control] [--json] [--fail-on-contract-mismatch] [--token <token>] [--ca-file <pem>] [--server-name <name>] [--plaintext]")
			}
			if cfg.target != "" {
				return usageError("Usage: arbiter status-issues [target] [--surface runtime|agent|control] [--json] [--fail-on-contract-mismatch] [--token <token>] [--ca-file <pem>] [--server-name <name>] [--plaintext]")
			}
			cfg.target = args[i]
		}
	}
	return statusIssuesCmd(cfg, surface)
}

func runExplore(args []string) error {
	path := ""
	if len(args) > 0 {
		path = args[0]
	}
	return exploreCmd(path)
}

type serveConfig struct {
	grpcAddr           string
	statusAddr         string
	auditFile          string
	bundleFile         string
	overridesFile      string
	dataDir            string
	logLevel           string
	authTokens         []string
	authTokenFile      string
	tlsCertFile        string
	tlsKeyFile         string
	tlsClientCAFile    string
	maxRecvBytes       int
	maxSendBytes       int
	sessionTTL         time.Duration
	sessionMax         int
	sessionMaxPerOwner int
	rateLimitRPM       int
	rateLimitBurst     int
	ephemeral          bool
}

type remoteInspectConfig struct {
	target                 string
	token                  string
	caFile                 string
	serverName             string
	forceInsecure          bool
	jsonOut                bool
	failOnIssues           bool
	failOnContractMismatch bool
}

func parseRemoteInspectConfig(args []string) remoteInspectConfig {
	cfg := remoteInspectConfig{target: args[0]}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--json":
			cfg.jsonOut = true
		case "--fail-on-issues":
			cfg.failOnIssues = true
		case "--fail-on-contract-mismatch":
			cfg.failOnContractMismatch = true
		case "--token":
			if i+1 < len(args) {
				cfg.token = args[i+1]
				i++
			}
		case "--ca-file":
			if i+1 < len(args) {
				cfg.caFile = args[i+1]
				i++
			}
		case "--server-name":
			if i+1 < len(args) {
				cfg.serverName = args[i+1]
				i++
			}
		case "--plaintext":
			cfg.forceInsecure = true
		}
	}
	return cfg
}

func runServe(args []string) error {
	cfg := serveConfig{
		grpcAddr:           "127.0.0.1:8081",
		statusAddr:         "127.0.0.1:8082",
		logLevel:           "info",
		maxRecvBytes:       4 << 20,
		maxSendBytes:       4 << 20,
		sessionTTL:         30 * time.Minute,
		sessionMax:         10_000,
		sessionMaxPerOwner: 100,
		rateLimitRPM:       600_000,
		rateLimitBurst:     20_000,
	}
	for i := 0; i < len(args); i++ {
		if args[i] == "--grpc" && i+1 < len(args) {
			cfg.grpcAddr = args[i+1]
			i++
		}
		if args[i] == "--status" && i+1 < len(args) {
			cfg.statusAddr = args[i+1]
			i++
		}
		if args[i] == "--audit-file" && i+1 < len(args) {
			cfg.auditFile = args[i+1]
			i++
		}
		if args[i] == "--bundle-file" && i+1 < len(args) {
			cfg.bundleFile = args[i+1]
			i++
		}
		if args[i] == "--overrides-file" && i+1 < len(args) {
			cfg.overridesFile = args[i+1]
			i++
		}
		if args[i] == "--log-level" && i+1 < len(args) {
			cfg.logLevel = args[i+1]
			i++
		}
		if args[i] == "--data-dir" && i+1 < len(args) {
			cfg.dataDir = args[i+1]
			i++
		}
		if args[i] == "--ephemeral" {
			cfg.ephemeral = true
		}
		if args[i] == "--auth-token" && i+1 < len(args) {
			cfg.authTokens = append(cfg.authTokens, args[i+1])
			i++
		}
		if args[i] == "--auth-token-file" && i+1 < len(args) {
			cfg.authTokenFile = args[i+1]
			i++
		}
		if args[i] == "--tls-cert" && i+1 < len(args) {
			cfg.tlsCertFile = args[i+1]
			i++
		}
		if args[i] == "--tls-key" && i+1 < len(args) {
			cfg.tlsKeyFile = args[i+1]
			i++
		}
		if args[i] == "--tls-client-ca" && i+1 < len(args) {
			cfg.tlsClientCAFile = args[i+1]
			i++
		}
		if args[i] == "--max-recv-bytes" && i+1 < len(args) {
			value, err := parseIntFlag("--max-recv-bytes", args[i+1], 1)
			if err != nil {
				return err
			}
			cfg.maxRecvBytes = value
			i++
		}
		if args[i] == "--max-send-bytes" && i+1 < len(args) {
			value, err := parseIntFlag("--max-send-bytes", args[i+1], 1)
			if err != nil {
				return err
			}
			cfg.maxSendBytes = value
			i++
		}
		if args[i] == "--session-ttl" && i+1 < len(args) {
			value, err := time.ParseDuration(args[i+1])
			if err != nil {
				return fmt.Errorf("parse --session-ttl: %w", err)
			}
			cfg.sessionTTL = value
			i++
		}
		if args[i] == "--session-max" && i+1 < len(args) {
			value, err := parseIntFlag("--session-max", args[i+1], 0)
			if err != nil {
				return err
			}
			cfg.sessionMax = value
			i++
		}
		if args[i] == "--session-max-per-owner" && i+1 < len(args) {
			value, err := parseIntFlag("--session-max-per-owner", args[i+1], 0)
			if err != nil {
				return err
			}
			cfg.sessionMaxPerOwner = value
			i++
		}
		if args[i] == "--rate-limit-rpm" && i+1 < len(args) {
			value, err := parseIntFlag("--rate-limit-rpm", args[i+1], 0)
			if err != nil {
				return err
			}
			cfg.rateLimitRPM = value
			i++
		}
		if args[i] == "--rate-limit-burst" && i+1 < len(args) {
			value, err := parseIntFlag("--rate-limit-burst", args[i+1], 0)
			if err != nil {
				return err
			}
			cfg.rateLimitBurst = value
			i++
		}
	}
	return serveCmd(cfg)
}

func formatCLIError(err error) string {
	if err == nil {
		return ""
	}
	// Check for multi-error (joined errors). Collect all diagnostic lines.
	if multi := collectDiagnostics(err); len(multi) > 0 {
		return strings.Join(multi, "\n")
	}
	if msg, ok := diagnosticString(err); ok {
		return msg
	}
	return fmt.Sprintf("error: %v", err)
}

func collectDiagnostics(err error) []string {
	// Walk the full error chain looking for joined errors.
	type unwrapMulti interface{ Unwrap() []error }
	if joined, ok := findJoined(err); ok {
		subs := joined.Unwrap()
		var lines []string
		for _, sub := range subs {
			if msg, ok := diagnosticString(sub); ok {
				lines = append(lines, msg)
			}
		}
		return lines
	}
	return nil
}

func findJoined(err error) (interface{ Unwrap() []error }, bool) {
	for cur := err; cur != nil; {
		if j, ok := cur.(interface{ Unwrap() []error }); ok {
			return j, true
		}
		cur = errors.Unwrap(cur)
	}
	return nil, false
}

func diagnosticString(err error) (string, bool) {
	var diag *arbiter.DiagnosticError
	if errors.As(err, &diag) {
		return diag.Error(), true
	}
	for cur := err; cur != nil; cur = errors.Unwrap(cur) {
		if looksLikeDiagnostic(cur.Error()) {
			return cur.Error(), true
		}
	}
	return "", false
}

func looksLikeDiagnostic(message string) bool {
	_, rest, ok := strings.Cut(message, ":")
	if !ok {
		return false
	}
	first, rest, ok := strings.Cut(rest, ":")
	if !ok {
		return false
	}
	if _, err := strconv.Atoi(strings.TrimSpace(first)); err != nil {
		return false
	}
	if second, _, ok := strings.Cut(rest, ":"); ok {
		if _, err := strconv.Atoi(strings.TrimSpace(second)); err == nil {
			return true
		}
	}
	return true
}

func check(path string, strict bool, opts ...arbiter.Option) error {
	return checkWithProfile(path, strict, "", opts...)
}

// checkWithProfile is check plus an optional numeric profile gate ("integer").
func checkWithProfile(path string, strict bool, numericProfile string, opts ...arbiter.Option) error {
	unit, parsed, err := arbiter.LoadFileParsed(path)
	if err != nil {
		return fmt.Errorf("check %s: %w", path, err)
	}
	// Resolution-aware compile: resolves imports, binds any schema (--proto /
	// `input from proto`), type-checks, and collects warnings (dead code, etc.).
	// It sees the fully merged, import-resolved program, so its error is
	// authoritative — do not fall back to an import-blind validation pass.
	prog, err := arbiter.CompileFile(path, opts...)
	if err != nil {
		return fmt.Errorf("check %s: %w", path, err)
	}

	// Import-aware compile for the modality checks below: resolves imports and
	// inlines templates so cross-module flag/expert/template references are seen
	// (CompileFullParsed would be import-blind and reject them).
	full, err := arbiter.CompileFullFile(path)
	if err != nil {
		return fmt.Errorf("check %s: %w", path, arbiter.WrapFileError(unit, err))
	}
	if _, err := flags.LoadParsed(parsed, full); err != nil {
		return fmt.Errorf("check %s: %w", path, arbiter.WrapFileError(unit, err))
	}
	expertProg, err := expert.CompileParsed(parsed, full)
	if err != nil {
		return fmt.Errorf("check %s: %w", path, arbiter.WrapFileError(unit, err))
	}
	if numericProfile == "integer" {
		// The expert program checks its own ruleset and the segments it holds;
		// the plain ruleset and its segments are checked here. A finding that
		// two checks both report is printed once, in a fixed order.
		var violations []compiler.NumericViolation
		violations = append(violations, compiler.CheckIntegerProfile(full.Ruleset)...)
		if full.Segments != nil {
			segs := full.Segments.All()
			sort.Slice(segs, func(i, j int) bool { return segs[i].Name < segs[j].Name })
			for _, seg := range segs {
				for _, v := range compiler.CheckIntegerProfile(seg.Ruleset) {
					v.Rule = "segment " + seg.Name
					violations = append(violations, v)
				}
			}
		}
		violations = append(violations, expertProg.NumericProfileViolations()...)
		seen := make(map[string]bool)
		failures := 0
		for _, v := range violations {
			key := fmt.Sprintf("%s|%d|%s|%v", v.Rule, v.Offset, v.Message, v.Warning)
			if seen[key] {
				continue
			}
			seen[key] = true
			if v.Warning {
				fmt.Fprintf(os.Stderr, "%s: warning: numeric profile integer: %s\n", path, v)
				continue
			}
			failures++
			fmt.Fprintf(os.Stderr, "%s: numeric profile integer: %s\n", path, v)
		}
		if failures > 0 {
			return fmt.Errorf("check %s: %d violation(s) of the integer numeric profile", path, failures)
		}
	}

	// Surface non-fatal warnings; --strict turns them into a CI failure.
	for _, w := range prog.Warnings {
		fmt.Fprintf(os.Stderr, "%s:%d:%d: warning: %s\n", path, w.Line, w.Col, w.Message)
	}
	if strict && len(prog.Warnings) > 0 {
		return fmt.Errorf("check %s: %d warning(s) reported with --strict", path, len(prog.Warnings))
	}

	fmt.Fprintf(os.Stderr, "%s: ok\n", path)
	return nil
}

func compileCmd(path string) error {
	full, err := arbiter.CompileFullFile(path)
	if err != nil {
		return fmt.Errorf("compile %s: %w", path, err)
	}
	rs := full.Ruleset

	fmt.Printf("compiled %s\n", path)
	fmt.Printf("  rules:        %d\n", len(rs.Rules))
	fmt.Printf("  strategies:   %d\n", full.Strategies.Count())
	fmt.Printf("  workers:      %d\n", len(full.Workers))
	fmt.Printf("  arbiters:     %d\n", len(full.Arbiters))
	fmt.Printf("  actions:      %d\n", len(rs.Actions))
	fmt.Printf("  instructions: %d bytes\n", len(rs.Instructions))
	fmt.Printf("  strings:      %d\n", rs.Constants.StringCount())
	fmt.Printf("  numbers:      %d\n", rs.Constants.NumberCount())
	if cost, rule := rs.WorstCaseCost(0); rule != "" {
		fmt.Printf("  worst-case cost: %d instr (rule %s)\n", cost, rule)
	}
	return nil
}

func evalCmd(path, dataJSON string, explain bool, opts ...arbiter.Option) error {
	prog, err := arbiter.CompileFile(path, opts...)
	if err != nil {
		return fmt.Errorf("compile %s: %w", path, err)
	}

	printMatched := func(matched []vm.MatchedRule) {
		if len(matched) == 0 {
			fmt.Println("no rules matched")
			return
		}
		for _, m := range matched {
			tag := "matched"
			if m.Fallback {
				tag = "fallback"
			}
			fmt.Printf("[%s] %s -> %s", tag, m.Name, m.Action)
			if len(m.Params) > 0 {
				out, _ := json.Marshal(m.Params)
				fmt.Printf(" %s", out)
			}
			fmt.Println()
		}
	}

	var ctx map[string]any
	if err := json.Unmarshal([]byte(dataJSON), &ctx); err != nil {
		return fmt.Errorf("parse data: %w", err)
	}
	// Evaluate with full governance so the CLI reflects the same semantics as
	// the server, the SDK, and `arbiter test`, all of which use EvalGoverned.
	// Plain (ungoverned) Eval would silently ignore segment/prereq gates and
	// report matches that production would block.
	matched, trace, err := arbiter.EvalGoverned(prog, arbiter.DataFromMap(ctx, prog), prog.Segments, ctx)
	if err != nil {
		return fmt.Errorf("eval: %w", err)
	}
	printMatched(matched)
	if !explain {
		return nil
	}
	// --explain additionally renders the arbitrace as an audit tree.
	fmt.Println("arbitrace:")
	for _, s := range trace.Steps {
		disp := s.Disposition
		if disp == "" {
			if s.Result {
				disp = "passed"
			} else {
				disp = "blocked"
			}
		}
		line := fmt.Sprintf("  [%-8s] %s", disp, s.Check)
		if s.Detail != "" {
			line += " — " + s.Detail
		}
		fmt.Println(line)
	}
	return nil
}

func strategyCmd(path, name, dataJSON string) error {
	full, err := arbiter.CompileFullFile(path)
	if err != nil {
		return fmt.Errorf("load strategies: %w", err)
	}
	strategies := full.Strategies
	if name == "" {
		names := strategies.Names()
		switch len(names) {
		case 0:
			return fmt.Errorf("bundle contains no strategies")
		case 1:
			name = names[0]
		default:
			return fmt.Errorf("bundle contains multiple strategies; use --name (%s)", strings.Join(names, ", "))
		}
	}

	var ctx map[string]any
	if err := json.Unmarshal([]byte(dataJSON), &ctx); err != nil {
		return fmt.Errorf("parse data: %w", err)
	}

	result, err := arbiter.EvalStrategy(full, name, ctx)
	if err != nil {
		return fmt.Errorf("strategy %s: %w", name, err)
	}
	blob, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal strategy result: %w", err)
	}
	fmt.Println(string(blob))
	return nil
}

func expertCmd(path, envelopeJSON, factsJSON string) error {
	program, err := expert.CompileFile(path)
	if err != nil {
		return fmt.Errorf("compile expert rules: %w", err)
	}

	var envelope map[string]any
	if err := json.Unmarshal([]byte(envelopeJSON), &envelope); err != nil {
		return fmt.Errorf("parse envelope: %w", err)
	}

	facts, err := parseFactsJSON(factsJSON)
	if err != nil {
		return err
	}

	result, err := expert.NewSession(program, envelope, facts, expert.Options{}).Run(context.Background())
	if err != nil {
		return fmt.Errorf("run expert session: %w", err)
	}

	out := map[string]any{
		"outcomes":    result.Outcomes,
		"facts":       result.Facts,
		"activations": result.Activations,
		"rounds":      result.Rounds,
		"mutations":   result.Mutations,
		"stop_reason": result.StopReason,
	}
	blob, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session result: %w", err)
	}
	fmt.Println(string(blob))
	return nil
}

func testCmd(path string, verbose, coverage bool, threshold float64) error {
	paths := []string(nil)
	if path == "" {
		files, err := filepath.Glob("*.test.arb")
		if err != nil {
			return fmt.Errorf("resolve tests: %w", err)
		}
		if len(files) == 0 {
			return fmt.Errorf("no .test.arb files found; create one next to your .arb bundle (e.g. pricing.test.arb for pricing.arb)")
		}
		paths = files
	} else {
		paths = append(paths, path)
	}

	totalPassed := 0
	totalFailed := 0
	totalRules := 0
	coveredRules := 0
	var uncovered []string
	for _, item := range paths {
		result, err := arbtest.RunFile(item, arbtest.Options{Verbose: verbose})
		if err != nil {
			return fmt.Errorf("test %s: %w", item, err)
		}
		printTestResult(result)
		totalPassed += result.Passed
		totalFailed += result.Failed
		totalRules += result.Coverage.Total
		coveredRules += len(result.Coverage.Covered)
		for _, u := range result.Coverage.Uncovered {
			uncovered = append(uncovered, fmt.Sprintf("%s: %s", result.Bundle, u))
		}
	}

	fmt.Printf("test summary: %d passed, %d failed\n", totalPassed, totalFailed)

	if coverage {
		pct := 100.0
		if totalRules > 0 {
			pct = float64(coveredRules) / float64(totalRules) * 100
		}
		fmt.Printf("coverage: %d/%d rules (%.1f%%)\n", coveredRules, totalRules, pct)
		for _, u := range uncovered {
			fmt.Printf("  uncovered rule %s\n", u)
		}
		if threshold >= 0 && pct < threshold {
			return fmt.Errorf("coverage %.1f%% is below threshold %.1f%%", pct, threshold)
		}
	}

	if totalFailed > 0 {
		return fmt.Errorf("%d test cases failed", totalFailed)
	}
	return nil
}

func printTestResult(result *arbtest.FileResult) {
	if result == nil {
		return
	}
	fmt.Printf("%s: %d passed, %d failed\n", result.File, result.Passed, result.Failed)
	for _, item := range result.Cases {
		if !result.Verbose && item.Passed {
			continue
		}
		status := "PASS"
		if !item.Passed {
			status = "FAIL"
		}
		fmt.Printf("  [%s] %s %q\n", status, item.Kind, item.Name)
		for _, detail := range item.Details {
			fmt.Printf("    %s\n", detail)
		}
		if item.Error != "" {
			fmt.Printf("    error: %s\n", item.Error)
		}
	}
}

func exploreCmd(path string) error {
	if path == "" {
		files, err := filepath.Glob("*.arb")
		if err != nil {
			return fmt.Errorf("resolve bundle: %w", err)
		}
		switch len(files) {
		case 0:
			return fmt.Errorf("explore requires a bundle path when the current directory has no .arb files")
		case 1:
			path = files[0]
		default:
			return fmt.Errorf("explore requires a bundle path when the current directory has multiple .arb files")
		}
	}

	summary, err := explorepkg.BuildSummaryFile(path)
	if err != nil {
		return fmt.Errorf("explore %s: %w", path, err)
	}
	blob, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal explore summary: %w", err)
	}
	fmt.Println(string(blob))
	return nil
}

func parseFactsJSON(factsJSON string) ([]expert.Fact, error) {
	if factsJSON == "" {
		return nil, nil
	}
	var facts []expert.Fact
	if err := json.Unmarshal([]byte(factsJSON), &facts); err != nil {
		return nil, fmt.Errorf("parse facts: %w", err)
	}
	return facts, nil
}

// importRuleJSON is the expected JSON structure for each rule in the input file.
type importRuleJSON struct {
	Name      string `json:"name"`
	Priority  int    `json:"priority"`
	Condition any    `json:"condition"`
	Action    any    `json:"action,omitempty"`
	Fallback  any    `json:"fallback,omitempty"`
}

func importCmd(path, outPath string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	// Try parsing as the TranspileResult format (with "rules" key)
	var wrapper struct {
		Rules []importRuleJSON `json:"rules"`
	}
	if err := json.Unmarshal(data, &wrapper); err == nil && len(wrapper.Rules) > 0 {
		return importRules(wrapper.Rules, outPath)
	}

	// Try parsing as a bare array of rules
	var ruleArr []importRuleJSON
	if err := json.Unmarshal(data, &ruleArr); err == nil && len(ruleArr) > 0 {
		return importRules(ruleArr, outPath)
	}

	// Try parsing as a single rule
	var single importRuleJSON
	if err := json.Unmarshal(data, &single); err == nil && single.Name != "" {
		return importRules([]importRuleJSON{single}, outPath)
	}

	return fmt.Errorf("cannot parse %s: expected Arishem JSON with rules array, rule array, or single rule", path)
}

func serveCmd(cfg serveConfig) error {
	logger := observability.NewLogger(observability.ParseLevel(cfg.logLevel))

	bundleFile, overridesFile, persistenceDir, err := resolveServePersistence(cfg.bundleFile, cfg.overridesFile, cfg.dataDir, cfg.ephemeral)
	if err != nil {
		return err
	}

	tokens, err := grpcutil.LoadAuthTokens(cfg.authTokens, cfg.authTokenFile)
	if err != nil {
		return err
	}
	tlsConfig, err := grpcutil.LoadServerTLSConfig(cfg.tlsCertFile, cfg.tlsKeyFile, cfg.tlsClientCAFile)
	if err != nil {
		return err
	}

	lis, err := net.Listen("tcp", cfg.grpcAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.grpcAddr, err)
	}

	var sink audit.Sink = audit.NopSink{}
	var closer interface{ Close() error }
	auditTracker := newControlAuditTracker(false, "discard", false, "")
	if cfg.auditFile != "" {
		fileSink, err := audit.NewJSONLSink(cfg.auditFile)
		if err != nil {
			return fmt.Errorf("open audit sink: %w", err)
		}
		sink = fileSink
		closer = fileSink
		auditTracker = newControlAuditTracker(true, "jsonl", true, cfg.auditFile)
	}
	sink = newTrackedAuditSink(sink, auditTracker, logger)
	if closer != nil {
		defer closer.Close()
	}

	registry := grpcserver.NewRegistry()
	if bundleFile != "" {
		registry, err = grpcserver.NewFileRegistry(bundleFile)
		if err != nil {
			return fmt.Errorf("open bundle registry: %w", err)
		}
	}
	store := overrides.NewStore()
	if overridesFile != "" {
		store, err = overrides.NewFileStore(overridesFile)
		if err != nil {
			return fmt.Errorf("open override store: %w", err)
		}
	}

	sessions := grpcserver.NewSessionStore()
	sessions.SetTTL(cfg.sessionTTL)
	sessions.SetMaxCount(cfg.sessionMax)
	sessions.SetMaxPerOwner(cfg.sessionMaxPerOwner)
	controlTransport := newControlListenerTransport(cfg.grpcAddr, tokens, tlsConfig)
	statusSource := controlStatusSource{
		registry:      registry,
		store:         store,
		sessions:      sessions,
		transport:     controlTransport,
		bundleFile:    bundleFile,
		overridesFile: overridesFile,
		audit:         auditTracker,
	}

	unaryInterceptors := []grpc.UnaryServerInterceptor{
		grpcserver.UnaryRecoveryInterceptor(logger),
	}
	streamInterceptors := []grpc.StreamServerInterceptor{
		grpcserver.StreamRecoveryInterceptor(logger),
	}
	if limiter := grpcserver.NewRateLimiter(cfg.rateLimitRPM, cfg.rateLimitBurst); limiter != nil {
		unaryInterceptors = append(unaryInterceptors, limiter.UnaryServerInterceptor())
		streamInterceptors = append(streamInterceptors, limiter.StreamServerInterceptor())
	}
	if len(tokens) > 0 {
		auth, err := grpcserver.NewStaticTokenAuth(tokens)
		if err != nil {
			return err
		}
		unaryInterceptors = append(unaryInterceptors, auth.UnaryServerInterceptor())
		streamInterceptors = append(streamInterceptors, auth.StreamServerInterceptor())
	}

	serverOptions := []grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.MaxRecvMsgSize(cfg.maxRecvBytes),
		grpc.MaxSendMsgSize(cfg.maxSendBytes),
	}
	if len(unaryInterceptors) > 0 {
		serverOptions = append(serverOptions, grpc.ChainUnaryInterceptor(unaryInterceptors...))
	}
	if len(streamInterceptors) > 0 {
		serverOptions = append(serverOptions, grpc.ChainStreamInterceptor(streamInterceptors...))
	}
	if tlsConfig != nil {
		serverOptions = append(serverOptions, grpc.Creds(credentials.NewTLS(tlsConfig)))
	}

	grpcSrv := grpc.NewServer(serverOptions...)
	controlServer := grpcserver.NewServerWithLoggerAndSessions(registry, store, sink, logger, sessions)
	arbiterv1.RegisterArbiterServiceServer(grpcSrv, controlServer)
	arbiterv1.RegisterControlServiceServer(grpcSrv, newControlRPCServer(statusSource))

	var statusSrv *http.Server
	if cfg.statusAddr != "" {
		reg := prometheus.NewRegistry()
		grpcserver.RegisterMetrics(reg)
		statusSrv = grpcserver.NewHTTPServerWithStatusAndReadinessAndCatalog(
			cfg.statusAddr,
			reg,
			func() any {
				return statusSource.Payload()
			},
			func() (bool, string) {
				payload := statusSource.Payload()
				return payload.Readiness.Ready, payload.Readiness.Reason
			},
			func() any {
				return statusview.CatalogForSurface(statusview.SurfaceControl)
			},
		)
		statusSrv.ReadHeaderTimeout = 5 * time.Second
		go func() {
			logger.Info("arbiter status listening", "addr", cfg.statusAddr)
			if err := statusSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("arbiter status server error", observability.KeyError, err.Error())
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = statusSrv.Shutdown(shutdownCtx)
		}()
	}

	if grpcutil.IsPublicListenAddr(cfg.grpcAddr) && tlsConfig == nil && len(tokens) == 0 {
		logger.Warn("arbiter gRPC listener is public without TLS or auth", "addr", cfg.grpcAddr)
	}
	logger.Info(
		"arbiter gRPC listening",
		"addr", cfg.grpcAddr,
		"status_addr", cfg.statusAddr,
		"persistent_state", persistenceDir != "",
		"state_dir", persistenceDir,
		"auth_enabled", len(tokens) > 0,
		"tls_enabled", tlsConfig != nil,
		"max_recv_bytes", cfg.maxRecvBytes,
		"max_send_bytes", cfg.maxSendBytes,
		"session_ttl", cfg.sessionTTL.String(),
		"session_max", cfg.sessionMax,
		"session_max_per_owner", cfg.sessionMaxPerOwner,
		"rate_limit_rpm", cfg.rateLimitRPM,
		"rate_limit_burst", cfg.rateLimitBurst,
	)
	return grpcSrv.Serve(lis)
}

func runtimeCapabilitiesCmd(cfg remoteInspectConfig) error {
	conn, _, err := grpcutil.Dial(grpcutil.DialConfig{
		Target:        cfg.target,
		Token:         cfg.token,
		CAFile:        cfg.caFile,
		ServerName:    cfg.serverName,
		ForceInsecure: cfg.forceInsecure,
	})
	if err != nil {
		return fmt.Errorf("connect runtime %s: %w", strings.TrimSpace(cfg.target), err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := arbiterv1.NewRuntimeServiceClient(conn).GetRuntimeCapabilities(ctx, &arbiterv1.GetRuntimeCapabilitiesRequest{})
	if err != nil {
		return fmt.Errorf("get runtime capabilities: %w", err)
	}
	if cfg.jsonOut {
		data, err := protojson.MarshalOptions{Indent: "  "}.Marshal(resp)
		if err != nil {
			return fmt.Errorf("marshal runtime capabilities: %w", err)
		}
		fmt.Println(string(data))
		return maybeWarnOrFailOperatorContract("runtime capabilities", cfg, protoOperatorInfo(resp.GetOperator()))
	}

	printRuntimeCapabilities(resp)
	return maybeWarnOrFailOperatorContract("runtime capabilities", cfg, protoOperatorInfo(resp.GetOperator()))
}

func runtimeStatusCmd(cfg remoteInspectConfig) error {
	conn, _, err := grpcutil.Dial(grpcutil.DialConfig{
		Target:        cfg.target,
		Token:         cfg.token,
		CAFile:        cfg.caFile,
		ServerName:    cfg.serverName,
		ForceInsecure: cfg.forceInsecure,
	})
	if err != nil {
		return fmt.Errorf("connect runtime %s: %w", strings.TrimSpace(cfg.target), err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := arbiterv1.NewRuntimeServiceClient(conn).GetRuntimeStatus(ctx, &arbiterv1.GetRuntimeStatusRequest{})
	if err != nil {
		return fmt.Errorf("get runtime status: %w", err)
	}
	if cfg.jsonOut {
		data, err := protojson.MarshalOptions{Indent: "  "}.Marshal(resp)
		if err != nil {
			return fmt.Errorf("marshal runtime status: %w", err)
		}
		fmt.Println(string(data))
		if err := maybeWarnOrFailOperatorContract("runtime status", cfg, protoOperatorInfo(resp.GetOperator())); err != nil {
			return err
		}
		return failOnBlockingIssues("runtime status", cfg.failOnIssues, resp.GetIssues())
	}

	printRuntimeStatus(resp)
	if err := maybeWarnOrFailOperatorContract("runtime status", cfg, protoOperatorInfo(resp.GetOperator())); err != nil {
		return err
	}
	return failOnBlockingIssues("runtime status", cfg.failOnIssues, resp.GetIssues())
}

func agentStatusCmd(cfg remoteInspectConfig) error {
	conn, _, err := grpcutil.Dial(grpcutil.DialConfig{
		Target:        cfg.target,
		Token:         cfg.token,
		CAFile:        cfg.caFile,
		ServerName:    cfg.serverName,
		ForceInsecure: cfg.forceInsecure,
	})
	if err != nil {
		return fmt.Errorf("connect agent %s: %w", strings.TrimSpace(cfg.target), err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := arbiterv1.NewAgentServiceClient(conn).GetAgentStatus(ctx, &arbiterv1.GetAgentStatusRequest{})
	if err != nil {
		return fmt.Errorf("get agent status: %w", err)
	}
	if cfg.jsonOut {
		data, err := protojson.MarshalOptions{Indent: "  "}.Marshal(resp)
		if err != nil {
			return fmt.Errorf("marshal agent status: %w", err)
		}
		fmt.Println(string(data))
		if err := maybeWarnOrFailOperatorContract("agent status", cfg, protoOperatorInfo(resp.GetOperator())); err != nil {
			return err
		}
		return failOnBlockingIssues("agent status", cfg.failOnIssues, resp.GetIssues())
	}

	printAgentStatus(resp)
	if err := maybeWarnOrFailOperatorContract("agent status", cfg, protoOperatorInfo(resp.GetOperator())); err != nil {
		return err
	}
	return failOnBlockingIssues("agent status", cfg.failOnIssues, resp.GetIssues())
}

func controlStatusCmd(cfg remoteInspectConfig) error {
	conn, _, err := grpcutil.Dial(grpcutil.DialConfig{
		Target:        cfg.target,
		Token:         cfg.token,
		CAFile:        cfg.caFile,
		ServerName:    cfg.serverName,
		ForceInsecure: cfg.forceInsecure,
	})
	if err != nil {
		return fmt.Errorf("connect control plane %s: %w", strings.TrimSpace(cfg.target), err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := arbiterv1.NewControlServiceClient(conn).GetControlStatus(ctx, &arbiterv1.GetControlStatusRequest{})
	if err != nil {
		return fmt.Errorf("get control status: %w", err)
	}
	if cfg.jsonOut {
		data, err := protojson.MarshalOptions{Indent: "  "}.Marshal(resp)
		if err != nil {
			return fmt.Errorf("marshal control status: %w", err)
		}
		fmt.Println(string(data))
		if err := maybeWarnOrFailOperatorContract("control status", cfg, protoOperatorInfo(resp.GetOperator())); err != nil {
			return err
		}
		return failOnBlockingIssues("control status", cfg.failOnIssues, resp.GetIssues())
	}

	printControlStatus(resp)
	if err := maybeWarnOrFailOperatorContract("control status", cfg, protoOperatorInfo(resp.GetOperator())); err != nil {
		return err
	}
	return failOnBlockingIssues("control status", cfg.failOnIssues, resp.GetIssues())
}

func statusIssuesCmd(cfg remoteInspectConfig, surface string) error {
	if _, err := normalizeStatusSurface(surface); err != nil {
		return err
	}

	var catalog statusview.Catalog
	if strings.TrimSpace(cfg.target) == "" {
		if strings.TrimSpace(surface) == "" {
			catalog = statusview.CatalogAll()
		} else {
			normalized, _ := normalizeStatusSurface(surface)
			catalog = statusview.CatalogForSurface(normalized)
		}
	} else {
		remoteCatalog, err := remoteStatusIssueCatalog(cfg, surface)
		if err != nil {
			return err
		}
		catalog = remoteCatalog
	}

	if cfg.jsonOut {
		data, err := json.MarshalIndent(catalog, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal status issues: %w", err)
		}
		fmt.Println(string(data))
		return maybeWarnOrFailOperatorContract("status issue catalog", cfg, catalog.Operator)
	}
	printStatusIssueCatalog(catalog)
	return maybeWarnOrFailOperatorContract("status issue catalog", cfg, catalog.Operator)
}

func normalizeStatusSurface(surface string) (statusview.Surface, error) {
	switch strings.TrimSpace(surface) {
	case "":
		return "", nil
	case string(statusview.SurfaceRuntime):
		return statusview.SurfaceRuntime, nil
	case string(statusview.SurfaceAgent):
		return statusview.SurfaceAgent, nil
	case string(statusview.SurfaceControl):
		return statusview.SurfaceControl, nil
	default:
		return "", fmt.Errorf("unknown status surface %q", surface)
	}
}

func remoteStatusIssueCatalog(cfg remoteInspectConfig, surfaceHint string) (statusview.Catalog, error) {
	conn, _, err := grpcutil.Dial(grpcutil.DialConfig{
		Target:        cfg.target,
		Token:         cfg.token,
		CAFile:        cfg.caFile,
		ServerName:    cfg.serverName,
		ForceInsecure: cfg.forceInsecure,
	})
	if err != nil {
		return statusview.Catalog{}, fmt.Errorf("connect status surface %s: %w", strings.TrimSpace(cfg.target), err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	order := remoteStatusIssueServiceOrder(surfaceHint)
	for _, candidate := range order {
		resp, err := fetchRemoteStatusIssueCatalog(ctx, conn, candidate)
		if err == nil {
			catalog := protoStatusIssueCatalog(resp)
			if catalog.Surface == "" {
				catalog.Surface = candidate
			}
			return catalog, nil
		}
		if grpcstatus.Code(err) == codes.Unimplemented {
			continue
		}
		return statusview.Catalog{}, fmt.Errorf("get status issue catalog from %s: %w", strings.TrimSpace(cfg.target), err)
	}
	return statusview.Catalog{}, fmt.Errorf("target %s does not expose a status issue catalog on runtime, agent, or control services", strings.TrimSpace(cfg.target))
}

func remoteStatusIssueServiceOrder(surfaceHint string) []statusview.Surface {
	preferred, err := normalizeStatusSurface(surfaceHint)
	if err != nil {
		return nil
	}
	if preferred != "" {
		return []statusview.Surface{preferred}
	}
	return []statusview.Surface{statusview.SurfaceRuntime, statusview.SurfaceAgent, statusview.SurfaceControl}
}

func fetchRemoteStatusIssueCatalog(ctx context.Context, conn *grpc.ClientConn, surface statusview.Surface) (*arbiterv1.GetStatusIssueCatalogResponse, error) {
	req := &arbiterv1.GetStatusIssueCatalogRequest{}
	switch surface {
	case statusview.SurfaceRuntime:
		return arbiterv1.NewRuntimeServiceClient(conn).GetStatusIssueCatalog(ctx, req)
	case statusview.SurfaceAgent:
		return arbiterv1.NewAgentServiceClient(conn).GetStatusIssueCatalog(ctx, req)
	case statusview.SurfaceControl:
		return arbiterv1.NewControlServiceClient(conn).GetStatusIssueCatalog(ctx, req)
	default:
		return nil, fmt.Errorf("unknown remote status surface %q", surface)
	}
}

func protoStatusIssueDefinitions(items []*arbiterv1.StatusIssueDefinition) []statusview.Definition {
	out := make([]statusview.Definition, 0, len(items))
	for _, item := range items {
		definition := statusview.Definition{
			Code:        statusview.Code(item.GetCode()),
			Severity:    statusview.Severity(item.GetSeverity()),
			Scope:       statusview.Scope(item.GetScope()),
			Blocking:    item.GetBlocking(),
			Description: item.GetDescription(),
		}
		for _, surface := range item.GetSurfaces() {
			definition.Surfaces = append(definition.Surfaces, statusview.Surface(surface))
		}
		out = append(out, definition)
	}
	return out
}

func protoStatusIssueCatalog(resp *arbiterv1.GetStatusIssueCatalogResponse) statusview.Catalog {
	catalog := statusview.Catalog{
		Surface:     statusview.Surface(resp.GetSurface()),
		Definitions: protoStatusIssueDefinitions(resp.GetDefinitions()),
	}
	if operator := resp.GetOperator(); operator != nil {
		catalog.Operator = protoOperatorInfo(operator)
	}
	return catalog
}

func protoOperatorInfo(operator *arbiterv1.OperatorIdentity) buildinfo.OperatorInfo {
	if operator == nil {
		return buildinfo.OperatorInfo{}
	}
	return buildinfo.OperatorInfo{
		Product:                 operator.GetProduct(),
		BuildVersion:            operator.GetBuildVersion(),
		OperatorContractVersion: operator.GetOperatorContractVersion(),
	}
}

func printRuntimeCapabilities(resp *arbiterv1.GetRuntimeCapabilitiesResponse) {
	fmt.Println("runtime surface")
	printOperatorBlock(protoOperatorInfo(resp.GetOperator()))
	fmt.Println("transport:")
	if control := resp.GetControlTransport(); control != nil {
		printRuntimeControlTransport("  control:", control)
	}
	if capabilityTransport := resp.GetCapabilityTransport(); capabilityTransport != nil {
		printRuntimeCapabilityTransport("  capability:", capabilityTransport)
	}

	fmt.Println("capabilities:")
	printRuntimePlugins("  plugins:", resp.GetPlugins())
	printRuntimeSources("  sources:", resp.GetSources())
	printRuntimeHandlers("  sinks:", resp.GetSinks())
	printRuntimeHandlers("  workers:", resp.GetWorkers())
}

func printRuntimeStatus(resp *arbiterv1.GetRuntimeStatusResponse) {
	fmt.Println("runtime status")
	printOperatorBlock(protoOperatorInfo(resp.GetOperator()))
	fmt.Println("readiness:")
	if readiness := resp.GetReadiness(); readiness != nil {
		fmt.Printf("  ready=%t\n", readiness.GetReady())
		if reason := strings.TrimSpace(readiness.GetReason()); reason != "" {
			fmt.Printf("  reason=%s\n", reason)
		}
	}
	printStatusIssues(resp.GetIssues())

	if transport := resp.GetTransport(); transport != nil {
		fmt.Println("transport:")
		printRuntimeControlTransport("  control:", transport.GetControl())
		printRuntimeCapabilityTransport("  capability:", transport.GetCapability())
	}

	if capabilities := resp.GetCapabilities(); capabilities != nil {
		fmt.Println("capabilities:")
		printRuntimePlugins("  plugins:", capabilities.GetPlugins())
		printRuntimeSources("  sources:", capabilities.GetSources())
		printRuntimeHandlers("  sinks:", capabilities.GetSinks())
		printRuntimeHandlers("  workers:", capabilities.GetWorkers())
	}

	if activity := resp.GetActivity(); activity != nil {
		fmt.Println("activity:")
		fmt.Printf("  ticks=%d errors=%d\n", activity.GetTicks(), activity.GetErrors())
		if lastTick := formatProtoTimestamp(activity.GetLastTick()); lastTick != "" {
			fmt.Printf("  last_tick=%s\n", lastTick)
		}
		if delivery := activity.GetDelivery(); delivery != nil {
			fmt.Printf("  delivery: delivered=%d enqueued=%d retried=%d\n", delivery.GetDelivered(), delivery.GetEnqueued(), delivery.GetRetried())
		}
		fmt.Println("  source_status:")
		if len(activity.GetSourceStatus()) == 0 {
			fmt.Println("    - none")
		} else {
			for _, item := range activity.GetSourceStatus() {
				fmt.Printf("    - %s alias=%s available=%t facts=%d failures=%d\n", item.GetTarget(), item.GetAlias(), item.GetAvailable(), item.GetFactCount(), item.GetConsecutiveFailures())
				if lastError := strings.TrimSpace(item.GetLastError()); lastError != "" {
					fmt.Printf("      last_error=%s\n", lastError)
				}
				printTimestamps("      ", map[string]*timestamppb.Timestamp{
					"last_attempt_at": item.GetLastAttemptAt(),
					"last_success_at": item.GetLastSuccessAt(),
					"next_retry_at":   item.GetNextRetryAt(),
				})
			}
		}
		fmt.Println("  sink_status:")
		if len(activity.GetSinkStatus()) == 0 {
			fmt.Println("    - none")
		} else {
			for _, item := range activity.GetSinkStatus() {
				fmt.Printf("    - %s kind=%s target=%s available=%t pending=%d ambiguous=%d failures=%d\n", item.GetKey(), item.GetKind(), item.GetTarget(), item.GetAvailable(), item.GetPending(), item.GetAmbiguous(), item.GetConsecutiveFailures())
				if lastError := strings.TrimSpace(item.GetLastError()); lastError != "" {
					fmt.Printf("      last_error=%s\n", lastError)
				}
				printTimestamps("      ", map[string]*timestamppb.Timestamp{
					"last_attempt_at": item.GetLastAttemptAt(),
					"last_success_at": item.GetLastSuccessAt(),
					"next_retry_at":   item.GetNextRetryAt(),
				})
			}
		}
	}
}

func printAgentStatus(resp *arbiterv1.GetAgentStatusResponse) {
	fmt.Println("agent status")
	printOperatorBlock(protoOperatorInfo(resp.GetOperator()))
	fmt.Println("readiness:")
	if readiness := resp.GetReadiness(); readiness != nil {
		fmt.Printf("  ready=%t\n", readiness.GetReady())
		if reason := strings.TrimSpace(readiness.GetReason()); reason != "" {
			fmt.Printf("  reason=%s\n", reason)
		}
		fmt.Printf("  targets=%d/%d max_staleness_ms=%d\n", readiness.GetReadyCount(), readiness.GetTargetCount(), readiness.GetMaxStalenessMs())
	}
	printStatusIssues(resp.GetIssues())

	if transport := resp.GetTransport(); transport != nil {
		fmt.Println("transport:")
		printAgentControlTransport("  control:", transport.GetControl())
		printAgentUpstreamTransport("  upstream:", transport.GetUpstream())
	}

	if sync := resp.GetSync(); sync != nil {
		fmt.Println("sync:")
		if primary := strings.TrimSpace(sync.GetPrimaryName()); primary != "" {
			fmt.Printf("  primary_name=%s\n", primary)
		}
		fmt.Printf("  errors: bundle=%d override=%d\n", sync.GetBundleErrorsTotal(), sync.GetOverrideErrorsTotal())
		fmt.Printf("  reconnects: bundle=%d override=%d\n", sync.GetBundleReconnectsTotal(), sync.GetOverrideReconnectsTotal())
		if lastError := strings.TrimSpace(sync.GetLastUpstreamError()); lastError != "" {
			fmt.Printf("  last_upstream_error=%s\n", lastError)
		}
		if ts := formatProtoTimestamp(sync.GetLastUpstreamErrorAt()); ts != "" {
			fmt.Printf("  last_upstream_error_at=%s\n", ts)
		}
		fmt.Println("  bundles:")
		if len(sync.GetBundles()) == 0 {
			fmt.Println("    - none")
		} else {
			for _, item := range sync.GetBundles() {
				fmt.Printf("    - %s bundle_id=%s bundle_watch=%t override_configured=%t override_watch=%t\n", item.GetName(), item.GetBundleId(), item.GetBundleWatchConnected(), item.GetOverrideConfigured(), item.GetOverrideWatchConnected())
				if checksum := strings.TrimSpace(item.GetChecksum()); checksum != "" {
					fmt.Printf("      checksum=%s\n", checksum)
				}
				fmt.Printf("      staleness_ms=%d override_staleness_ms=%d bundle_errors=%d override_errors=%d bundle_reconnects=%d override_reconnects=%d\n", item.GetStalenessMs(), item.GetOverrideStalenessMs(), item.GetBundleErrorsTotal(), item.GetOverrideErrorsTotal(), item.GetBundleReconnects(), item.GetOverrideReconnects())
				if lastBundleError := strings.TrimSpace(item.GetLastBundleError()); lastBundleError != "" {
					fmt.Printf("      last_bundle_error=%s\n", lastBundleError)
				}
				if lastOverrideError := strings.TrimSpace(item.GetLastOverrideError()); lastOverrideError != "" {
					fmt.Printf("      last_override_error=%s\n", lastOverrideError)
				}
				printTimestamps("      ", map[string]*timestamppb.Timestamp{
					"loaded_at":              item.GetLoadedAt(),
					"bundle_synced_at":       item.GetBundleSyncedAt(),
					"override_synced_at":     item.GetOverrideSyncedAt(),
					"last_bundle_error_at":   item.GetLastBundleErrorAt(),
					"last_override_error_at": item.GetLastOverrideErrorAt(),
				})
			}
		}
	}
}

func printControlStatus(resp *arbiterv1.GetControlStatusResponse) {
	fmt.Println("control status")
	printOperatorBlock(protoOperatorInfo(resp.GetOperator()))
	fmt.Println("readiness:")
	if readiness := resp.GetReadiness(); readiness != nil {
		fmt.Printf("  ready=%t\n", readiness.GetReady())
		if reason := strings.TrimSpace(readiness.GetReason()); reason != "" {
			fmt.Printf("  reason=%s\n", reason)
		}
	}
	printStatusIssues(resp.GetIssues())

	if transport := resp.GetTransport(); transport != nil {
		fmt.Println("transport:")
		printControlTransport("  control:", transport.GetControl())
	}

	if bundles := resp.GetBundles(); bundles != nil {
		fmt.Println("bundles:")
		fmt.Printf("  published_total=%d active_total=%d persisted=%t healthy=%t writes=%d errors=%d\n", bundles.GetPublishedTotal(), bundles.GetActiveTotal(), bundles.GetPersisted(), bundles.GetHealthy(), bundles.GetWritesTotal(), bundles.GetErrorsTotal())
		if file := strings.TrimSpace(bundles.GetFile()); file != "" {
			fmt.Printf("  file=%s\n", file)
		}
		if ts := formatProtoTimestamp(bundles.GetLastSuccessAt()); ts != "" {
			fmt.Printf("  last_success_at=%s\n", ts)
		}
		if lastError := strings.TrimSpace(bundles.GetLastError()); lastError != "" {
			fmt.Printf("  last_error=%s\n", lastError)
		}
		if ts := formatProtoTimestamp(bundles.GetLastErrorAt()); ts != "" {
			fmt.Printf("  last_error_at=%s\n", ts)
		}
		fmt.Println("  active:")
		if len(bundles.GetActive()) == 0 {
			fmt.Println("    - none")
		} else {
			for _, item := range bundles.GetActive() {
				fmt.Printf("    - %s bundle_id=%s versions=%d checksum=%s\n", item.GetName(), item.GetBundleId(), item.GetPublishedVersions(), item.GetChecksum())
				fmt.Printf("      rules=%d flags=%d expert_rules=%d strategies=%d\n", item.GetRuleCount(), item.GetFlagCount(), item.GetExpertRuleCount(), item.GetStrategyCount())
				if ts := formatProtoTimestamp(item.GetPublishedAt()); ts != "" {
					fmt.Printf("      published_at=%s\n", ts)
				}
			}
		}
	}

	if overrides := resp.GetOverrides(); overrides != nil {
		fmt.Println("overrides:")
		fmt.Printf("  bundle_total=%d rules=%d flags=%d flag_rules=%d strategies=%d persisted=%t healthy=%t writes=%d errors=%d\n", overrides.GetBundleTotal(), overrides.GetRules(), overrides.GetFlags(), overrides.GetFlagRules(), overrides.GetStrategies(), overrides.GetPersisted(), overrides.GetHealthy(), overrides.GetWritesTotal(), overrides.GetErrorsTotal())
		if file := strings.TrimSpace(overrides.GetFile()); file != "" {
			fmt.Printf("  file=%s\n", file)
		}
		if ts := formatProtoTimestamp(overrides.GetLastSuccessAt()); ts != "" {
			fmt.Printf("  last_success_at=%s\n", ts)
		}
		if lastError := strings.TrimSpace(overrides.GetLastError()); lastError != "" {
			fmt.Printf("  last_error=%s\n", lastError)
		}
		if ts := formatProtoTimestamp(overrides.GetLastErrorAt()); ts != "" {
			fmt.Printf("  last_error_at=%s\n", ts)
		}
		fmt.Println("  bundles:")
		if len(overrides.GetBundles()) == 0 {
			fmt.Println("    - none")
		} else {
			for _, item := range overrides.GetBundles() {
				label := item.GetName()
				if label == "" {
					label = item.GetBundleId()
				}
				fmt.Printf("    - %s bundle_id=%s rules=%d flags=%d flag_rules=%d strategies=%d\n", label, item.GetBundleId(), item.GetRules(), item.GetFlags(), item.GetFlagRules(), item.GetStrategies())
			}
		}
	}

	if sessions := resp.GetSessions(); sessions != nil {
		fmt.Println("sessions:")
		fmt.Printf("  active=%d ttl_ms=%d max_count=%d max_per_owner=%d\n", sessions.GetActive(), sessions.GetTtlMs(), sessions.GetMaxCount(), sessions.GetMaxPerOwner())
		fmt.Println("  bundles:")
		if len(sessions.GetBundles()) == 0 {
			fmt.Println("    - none")
		} else {
			for _, item := range sessions.GetBundles() {
				label := item.GetName()
				if label == "" {
					label = item.GetBundleId()
				}
				fmt.Printf("    - %s bundle_id=%s active=%d\n", label, item.GetBundleId(), item.GetActive())
			}
		}
	}

	if audit := resp.GetAudit(); audit != nil {
		fmt.Println("audit:")
		fmt.Printf("  configured=%t kind=%s durable=%t healthy=%t writes=%d errors=%d\n", audit.GetConfigured(), audit.GetKind(), audit.GetDurable(), audit.GetHealthy(), audit.GetWritesTotal(), audit.GetErrorsTotal())
		if file := strings.TrimSpace(audit.GetFile()); file != "" {
			fmt.Printf("  file=%s\n", file)
		}
		if ts := formatProtoTimestamp(audit.GetLastSuccessAt()); ts != "" {
			fmt.Printf("  last_success_at=%s\n", ts)
		}
		if lastError := strings.TrimSpace(audit.GetLastError()); lastError != "" {
			fmt.Printf("  last_error=%s\n", lastError)
		}
		if ts := formatProtoTimestamp(audit.GetLastErrorAt()); ts != "" {
			fmt.Printf("  last_error_at=%s\n", ts)
		}
	}
}

func printStatusIssueCatalog(catalog statusview.Catalog) {
	title := "status issue catalog"
	if trimmed := strings.TrimSpace(string(catalog.Surface)); trimmed != "" {
		title = fmt.Sprintf("%s (%s)", title, trimmed)
	}
	fmt.Println(title)
	printOperatorBlock(catalog.Operator)
	if len(catalog.Definitions) == 0 {
		fmt.Println("  - none")
		return
	}
	for _, item := range catalog.Definitions {
		surfaces := make([]string, 0, len(item.Surfaces))
		for _, surface := range item.Surfaces {
			surfaces = append(surfaces, string(surface))
		}
		fmt.Printf("  - code=%s severity=%s scope=%s blocking=%t surfaces=%s\n", item.Code, item.Severity, item.Scope, item.Blocking, strings.Join(surfaces, ","))
		fmt.Printf("    description=%s\n", item.Description)
	}
}

func printOperatorBlock(operator buildinfo.OperatorInfo) {
	fmt.Println("operator:")
	report := buildinfo.CheckOperator(operator)
	if !report.Available {
		fmt.Println("  unavailable")
	} else {
		fmt.Printf("  product=%s build_version=%s operator_contract_version=%s\n", operator.Product, operator.BuildVersion, operator.OperatorContractVersion)
	}
	status := "compatible"
	if !report.Available {
		status = "unknown"
	}
	if report.Available && !report.Compatible {
		status = "mismatch"
	}
	fmt.Printf("  contract=%s expected_product=%s expected_operator_contract_version=%s\n", status, report.ExpectedProduct, report.ExpectedOperatorContractVersion)
	if !report.Compatible {
		fmt.Printf("  note=%s\n", operatorCompatibilityMessage(report))
	}
}

func maybeWarnOrFailOperatorContract(subject string, cfg remoteInspectConfig, operator buildinfo.OperatorInfo) error {
	report := buildinfo.CheckOperator(operator)
	if report.Compatible {
		return nil
	}
	message := fmt.Sprintf("%s operator compatibility: %s", strings.TrimSpace(subject), operatorCompatibilityMessage(report))
	if cfg.failOnContractMismatch {
		return errors.New(message)
	}
	fmt.Fprintf(os.Stderr, "warning: %s\n", message)
	return nil
}

func operatorCompatibilityMessage(report buildinfo.OperatorCompatibility) string {
	switch {
	case !report.Available:
		return fmt.Sprintf("operator identity unavailable; expected product=%s operator_contract_version=%s", report.ExpectedProduct, report.ExpectedOperatorContractVersion)
	case report.Product != report.ExpectedProduct:
		return fmt.Sprintf("expected product=%s, got product=%s", report.ExpectedProduct, report.Product)
	case report.OperatorContractVersion != report.ExpectedOperatorContractVersion:
		return fmt.Sprintf("expected operator_contract_version=%s, got operator_contract_version=%s", report.ExpectedOperatorContractVersion, report.OperatorContractVersion)
	default:
		return "compatible"
	}
}

func printStatusIssues(issues []*arbiterv1.StatusIssue) {
	fmt.Println("issues:")
	if len(issues) == 0 {
		fmt.Println("  - none")
		return
	}
	for _, item := range issues {
		if item == nil {
			continue
		}
		line := fmt.Sprintf("  - severity=%s blocking=%t scope=%s", item.GetSeverity(), item.GetBlocking(), item.GetScope())
		if subject := strings.TrimSpace(item.GetSubject()); subject != "" {
			line += fmt.Sprintf(" subject=%s", subject)
		}
		if code := strings.TrimSpace(item.GetCode()); code != "" {
			line += fmt.Sprintf(" code=%s", code)
		}
		if message := strings.TrimSpace(item.GetMessage()); message != "" {
			line += fmt.Sprintf(" message=%s", message)
		}
		fmt.Println(line)
	}
}

func failOnBlockingIssues(subject string, enabled bool, issues []*arbiterv1.StatusIssue) error {
	if !enabled {
		return nil
	}
	count := blockingIssueCount(issues)
	if count == 0 {
		return nil
	}
	return fmt.Errorf("%s has %d blocking issue(s)", strings.TrimSpace(subject), count)
}

func blockingIssueCount(issues []*arbiterv1.StatusIssue) int {
	count := 0
	for _, item := range issues {
		if item != nil && item.GetBlocking() {
			count++
		}
	}
	return count
}

func printControlTransport(label string, control *arbiterv1.ControlListenerTransport) {
	if control == nil {
		return
	}
	fmt.Println(label)
	if control.GetEnabled() {
		fmt.Printf("    - %s\n", control.GetAddress())
		fmt.Printf("      auth=%t tls=%t mtls=%t public=%t\n", control.GetAuthEnabled(), control.GetTlsEnabled(), control.GetMutualTlsEnabled(), control.GetPublicListener())
		return
	}
	fmt.Println("    - disabled")
}

func printRuntimeControlTransport(label string, control *arbiterv1.RuntimeControlTransport) {
	if control == nil {
		return
	}
	fmt.Println(label)
	if control.GetEnabled() {
		fmt.Printf("    - %s\n", control.GetAddress())
		fmt.Printf("      auth=%t tls=%t mtls=%t public=%t\n", control.GetAuthEnabled(), control.GetTlsEnabled(), control.GetMutualTlsEnabled(), control.GetPublicListener())
		return
	}
	fmt.Println("    - disabled")
}

func printRuntimeCapabilityTransport(label string, capabilityTransport *arbiterv1.RuntimeCapabilityTransport) {
	if capabilityTransport == nil {
		return
	}
	fmt.Println(label)
	if capabilityTransport.GetConfigured() {
		fmt.Printf("    - %s\n", capabilityTransport.GetTarget())
		fmt.Printf("      auth=%t tls=%t\n", capabilityTransport.GetAuthEnabled(), capabilityTransport.GetTlsEnabled())
		if serverName := strings.TrimSpace(capabilityTransport.GetServerName()); serverName != "" {
			fmt.Printf("      server_name=%s\n", serverName)
		}
		return
	}
	fmt.Println("    - not configured")
}

func printRuntimePlugins(label string, plugins []*arbiterv1.RuntimePluginInfo) {
	fmt.Println(label)
	if len(plugins) == 0 {
		fmt.Println("    - none")
		return
	}
	for _, plugin := range plugins {
		name := plugin.GetName()
		if version := strings.TrimSpace(plugin.GetVersion()); version != "" {
			name += " (" + version + ")"
		}
		fmt.Printf("    - %s\n", name)
	}
}

func printRuntimeSources(label string, sources []*arbiterv1.RuntimeSourceCapability) {
	fmt.Println(label)
	if len(sources) == 0 {
		fmt.Println("    - none")
		return
	}
	for _, item := range sources {
		fmt.Printf("    - %s [%s]\n", item.GetScheme(), capabilityOwnerLabel(item.GetOwner()))
		if desc := strings.TrimSpace(item.GetDescription()); desc != "" {
			fmt.Printf("      %s\n", desc)
		}
	}
}

func printRuntimeHandlers(label string, handlers []*arbiterv1.RuntimeHandlerCapability) {
	fmt.Println(label)
	if len(handlers) == 0 {
		fmt.Println("    - none")
		return
	}
	for _, item := range handlers {
		fmt.Printf("    - %s [%s]\n", item.GetKind(), capabilityOwnerLabel(item.GetOwner()))
		if desc := strings.TrimSpace(item.GetDescription()); desc != "" {
			fmt.Printf("      %s\n", desc)
		}
	}
}

func printAgentControlTransport(label string, control *arbiterv1.AgentControlTransport) {
	if control == nil {
		return
	}
	fmt.Println(label)
	if control.GetEnabled() {
		fmt.Printf("    - %s\n", control.GetAddress())
		fmt.Printf("      auth=%t tls=%t mtls=%t public=%t\n", control.GetAuthEnabled(), control.GetTlsEnabled(), control.GetMutualTlsEnabled(), control.GetPublicListener())
		return
	}
	fmt.Println("    - disabled")
}

func printAgentUpstreamTransport(label string, upstream *arbiterv1.AgentUpstreamTransport) {
	if upstream == nil {
		return
	}
	fmt.Println(label)
	if upstream.GetConfigured() {
		fmt.Printf("    - %s\n", upstream.GetTarget())
		fmt.Printf("      auth=%t tls=%t\n", upstream.GetAuthEnabled(), upstream.GetTlsEnabled())
		if serverName := strings.TrimSpace(upstream.GetServerName()); serverName != "" {
			fmt.Printf("      server_name=%s\n", serverName)
		}
		return
	}
	fmt.Println("    - not configured")
}

func printTimestamps(prefix string, fields map[string]*timestamppb.Timestamp) {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if formatted := formatProtoTimestamp(fields[key]); formatted != "" {
			fmt.Printf("%s%s=%s\n", prefix, key, formatted)
		}
	}
}

func formatProtoTimestamp(value *timestamppb.Timestamp) string {
	if value == nil {
		return ""
	}
	return value.AsTime().UTC().Format(time.RFC3339)
}

func capabilityOwnerLabel(owner arbiterv1.CapabilityOwner) string {
	switch owner {
	case arbiterv1.CapabilityOwner_CAPABILITY_OWNER_CORE:
		return "core"
	case arbiterv1.CapabilityOwner_CAPABILITY_OWNER_HOST:
		return "host"
	case arbiterv1.CapabilityOwner_CAPABILITY_OWNER_PLUGIN:
		return "plugin"
	default:
		return "unspecified"
	}
}

func parseIntFlag(name, raw string, min int) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	if value < min {
		return 0, fmt.Errorf("%s must be >= %d", name, min)
	}
	return value, nil
}

func resolveServePersistence(bundleFile, overridesFile, dataDir string, ephemeral bool) (string, string, string, error) {
	if ephemeral {
		return bundleFile, overridesFile, "", nil
	}
	if bundleFile != "" && overridesFile != "" {
		return bundleFile, overridesFile, "", nil
	}
	if dataDir == "" {
		dir, err := os.UserConfigDir()
		if err != nil || dir == "" {
			dir = ".arbiter"
		} else {
			dir = filepath.Join(dir, "arbiter")
		}
		dataDir = dir
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", "", "", fmt.Errorf("create state dir: %w", err)
	}
	if bundleFile == "" {
		bundleFile = filepath.Join(dataDir, "bundles.json")
	}
	if overridesFile == "" {
		overridesFile = filepath.Join(dataDir, "overrides.json")
	}
	return bundleFile, overridesFile, dataDir, nil
}

func importRules(rules []importRuleJSON, outPath string) error {
	var arishemRules []decompile.ArishemRule

	for _, r := range rules {
		ar := decompile.ArishemRule{
			Name:     r.Name,
			Priority: r.Priority,
		}

		if r.Condition != nil {
			b, err := json.Marshal(r.Condition)
			if err != nil {
				return fmt.Errorf("rule %s: marshal condition: %w", r.Name, err)
			}
			ar.Condition = string(b)
		}
		if r.Action != nil {
			b, err := json.Marshal(r.Action)
			if err != nil {
				return fmt.Errorf("rule %s: marshal action: %w", r.Name, err)
			}
			ar.Action = string(b)
		}
		if r.Fallback != nil {
			b, err := json.Marshal(r.Fallback)
			if err != nil {
				return fmt.Errorf("rule %s: marshal fallback: %w", r.Name, err)
			}
			ar.Fallback = string(b)
		}

		arishemRules = append(arishemRules, ar)
	}

	arb, err := decompile.ArishemToArb(arishemRules)
	if err != nil {
		return fmt.Errorf("decompile: %w", err)
	}

	if outPath != "" {
		if err := os.WriteFile(outPath, []byte(arb), 0644); err != nil {
			return fmt.Errorf("write %s: %w", outPath, err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s (%d rules)\n", outPath, len(rules))
		return nil
	}

	fmt.Print(arb)
	return nil
}

func runFmt(args []string) error {
	check := false
	var paths []string
	for _, arg := range args {
		switch arg {
		case "--check":
			check = true
		case "--write":
			// Default behavior — explicit flag accepted for clarity.
		default:
			paths = append(paths, arg)
		}
	}
	if len(paths) == 0 {
		return usageError("Usage: arbiter fmt [--check] [--write] <file.arb> [file2.arb ...]")
	}
	changed := false
	for _, path := range paths {
		source, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		formatted := format.Format(source)
		if string(formatted) == string(source) {
			continue
		}
		changed = true
		if check {
			fmt.Fprintf(os.Stderr, "%s: needs formatting\n", path)
			continue
		}
		if err := os.WriteFile(path, formatted, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Fprintf(os.Stderr, "formatted %s\n", path)
	}
	if check && changed {
		return fmt.Errorf("files need formatting")
	}
	return nil
}

func runBundle(args []string) error {
	if len(args) < 1 {
		return usageError("Usage: arbiter bundle <file.arb> [-o output.arbb] [--risk-policy policy.arb] [--force] [--sign key.pem]\n       arbiter bundle --verify <file.arbb> --pub <public-key.pem>")
	}

	// Check for --verify mode first.
	verifyPath := ""
	pubKeyPath := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--verify":
			if i+1 < len(args) {
				verifyPath = args[i+1]
				i++
			}
		case "--pub":
			if i+1 < len(args) {
				pubKeyPath = args[i+1]
				i++
			}
		}
	}
	if verifyPath != "" {
		return verifyBundle(verifyPath, pubKeyPath)
	}

	path := args[0]
	outPath := ""
	riskPolicyPath := ""
	signKeyPath := ""
	force := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "-o":
			if i+1 < len(args) {
				outPath = args[i+1]
				i++
			}
		case "--risk-policy":
			if i+1 < len(args) {
				riskPolicyPath = args[i+1]
				i++
			}
		case "--sign":
			if i+1 < len(args) {
				signKeyPath = args[i+1]
				i++
			}
		case "--force":
			force = true
		}
	}
	prog, err := arbiter.CompileFile(path)
	if err != nil {
		return fmt.Errorf("bundle %s: %w", path, err)
	}

	// Analyze bundle for edge export safety using Arbiter policy.
	var policySource []byte
	if riskPolicyPath != "" {
		policySource, err = os.ReadFile(riskPolicyPath)
		if err != nil {
			return fmt.Errorf("read risk policy %s: %w", riskPolicyPath, err)
		}
	}
	result, err := bundle.Analyze(prog.Ruleset, policySource)
	if err != nil {
		return fmt.Errorf("analyze %s: %w", path, err)
	}

	// Print signals.
	for _, sig := range result.Signals {
		fmt.Fprintf(os.Stderr, "[signal] %s: count=%d\n", sig.Kind, sig.Count)
	}
	// Print decision.
	for _, reason := range result.Reasons {
		fmt.Fprintf(os.Stderr, "[%s] %s\n", result.Decision, reason)
	}
	if result.Decision == "block" && !force {
		return fmt.Errorf("edge export blocked by policy — use --force to override")
	}
	if result.Decision == "warn" {
		fmt.Fprintln(os.Stderr, "[warn] proceeding with warnings")
	}

	opts := bundle.ObfuscateOptions{
		HashRuleNames:       true,
		HashSegmentNames:    true,
		StripRolloutDetails: true,
		StripPrereqs:        true,
	}
	blob, err := bundle.MarshalObfuscated(prog.Ruleset, opts)
	if err != nil {
		return fmt.Errorf("bundle %s: %w", path, err)
	}

	if outPath == "" {
		outPath = strings.TrimSuffix(path, ".arb") + ".arbb"
	}
	if err := os.WriteFile(outPath, blob, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d rules, %d bytes, obfuscated)\n", outPath, len(prog.Ruleset.Rules), len(blob))

	// Sign into a detached sidecar (<bundle>.sig); the ARB1 bytes stay pristine.
	if signKeyPath != "" {
		privKey, err := loadPrivateKey(signKeyPath)
		if err != nil {
			return fmt.Errorf("load signing key: %w", err)
		}
		manifest := bundle.SignManifest(blob, privKey, filepath.Base(signKeyPath), buildinfo.Version)
		sidecar, err := manifest.MarshalSidecar()
		if err != nil {
			return fmt.Errorf("encode signature manifest: %w", err)
		}
		sigPath := outPath + ".sig"
		if err := os.WriteFile(sigPath, sidecar, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", sigPath, err)
		}
		fmt.Fprintf(os.Stderr, "[sign] wrote %s (Ed25519 detached signature)\n", sigPath)
	}
	return nil
}

// verifyBundle reads a signed .arbb file and verifies it against the given
// public key. Prints the verification result to stderr.
func verifyBundle(bundlePath, pubKeyPath string) error {
	if pubKeyPath == "" {
		return fmt.Errorf("--pub flag is required for --verify")
	}
	blob, err := os.ReadFile(bundlePath)
	if err != nil {
		return fmt.Errorf("read bundle %s: %w", bundlePath, err)
	}
	sigPath := bundlePath + ".sig"
	sidecar, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("read signature %s: %w", sigPath, err)
	}
	manifest, err := bundle.ParseSidecar(sidecar)
	if err != nil {
		return err
	}
	pubKey, err := loadPublicKey(pubKeyPath)
	if err != nil {
		return fmt.Errorf("load public key: %w", err)
	}
	if err := manifest.VerifyBlob(blob, pubKey); err != nil {
		return fmt.Errorf("verify %s: %w", bundlePath, err)
	}
	fmt.Fprintf(os.Stderr, "verified %s: signature valid (signer %q, %d bytes)\n", bundlePath, manifest.Signer, len(blob))
	return nil
}

// loadPrivateKey reads a PEM-encoded Ed25519 private key from disk.
// Accepts PEM files with type "PRIVATE KEY" or "ED25519 PRIVATE KEY"
// containing the raw 64-byte seed+key, or a raw 64-byte file.
func loadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block != nil {
		if len(block.Bytes) == ed25519.PrivateKeySize {
			return ed25519.PrivateKey(block.Bytes), nil
		}
		// Some PEM encodings wrap just the 32-byte seed.
		if len(block.Bytes) == ed25519.SeedSize {
			return ed25519.NewKeyFromSeed(block.Bytes), nil
		}
		return nil, fmt.Errorf("PEM block has unexpected size %d (want %d or %d)", len(block.Bytes), ed25519.PrivateKeySize, ed25519.SeedSize)
	}
	// Fall back to raw bytes.
	if len(data) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(data), nil
	}
	if len(data) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(data), nil
	}
	return nil, fmt.Errorf("file %s is not a valid Ed25519 private key (size %d)", path, len(data))
}

// loadPublicKey reads a PEM-encoded Ed25519 public key from disk.
// Accepts PEM files with type "PUBLIC KEY" or "ED25519 PUBLIC KEY"
// containing the raw 32-byte key, or a raw 32-byte file.
func loadPublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block != nil {
		if len(block.Bytes) == ed25519.PublicKeySize {
			return ed25519.PublicKey(block.Bytes), nil
		}
		return nil, fmt.Errorf("PEM block has unexpected size %d (want %d)", len(block.Bytes), ed25519.PublicKeySize)
	}
	if len(data) == ed25519.PublicKeySize {
		return ed25519.PublicKey(data), nil
	}
	return nil, fmt.Errorf("file %s is not a valid Ed25519 public key (size %d)", path, len(data))
}
