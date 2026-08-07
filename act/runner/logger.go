// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"

	"gitea.com/gitea/runner/act/common"

	"github.com/sirupsen/logrus"
	"golang.org/x/term"
)

const (
	// nocolor = 0
	red     = 31
	green   = 32
	yellow  = 33
	blue    = 34
	magenta = 35
	cyan    = 36
	gray    = 37
)

const (
	rawOutputField      = "raw_output"
	scriptLineCyanField = "script_line_cyan"
)

var (
	colors    []int
	nextColor int
	mux       sync.Mutex
)

func init() {
	nextColor = 0
	colors = []int{
		blue, yellow, green, magenta, red, gray, cyan,
	}
}

type masksContextKey string

const masksContextKeyVal = masksContextKey("logrus.FieldLogger")

// Logger returns the appropriate logger for current context
func Masks(ctx context.Context) *[]string {
	val := ctx.Value(masksContextKeyVal)
	if val != nil {
		if masks, ok := val.(*[]string); ok {
			return masks
		}
	}
	return &[]string{}
}

// WithMasks adds a value to the context for the logger
func WithMasks(ctx context.Context, masks *[]string) context.Context {
	return context.WithValue(ctx, masksContextKeyVal, masks)
}

type JobLoggerFactory interface {
	WithJobLogger() *logrus.Logger
}

type jobLoggerFactoryContextKey string

var jobLoggerFactoryContextKeyVal = (jobLoggerFactoryContextKey)("jobloggerkey")

func WithJobLoggerFactory(ctx context.Context, factory JobLoggerFactory) context.Context {
	return context.WithValue(ctx, jobLoggerFactoryContextKeyVal, factory)
}

// WithJobLogger attaches a new logger to context that is aware of steps
func WithJobLogger(ctx context.Context, jobID, jobName string, config *Config, masks *[]string, matrix map[string]any) context.Context {
	ctx = WithMasks(ctx, masks)

	var logger *logrus.Logger
	if jobLoggerFactory, ok := ctx.Value(jobLoggerFactoryContextKeyVal).(JobLoggerFactory); ok && jobLoggerFactory != nil {
		logger = jobLoggerFactory.WithJobLogger()
	} else {
		var formatter logrus.Formatter
		if config.JSONLogger {
			formatter = &logrus.JSONFormatter{}
		} else {
			mux.Lock()
			defer mux.Unlock()
			nextColor++
			formatter = &jobLogFormatter{
				color:          colors[nextColor%len(colors)],
				logPrefixJobID: config.LogPrefixJobID,
			}
		}

		logger = logrus.New()
		logger.SetOutput(os.Stdout)
		logger.SetLevel(logrus.GetLevel())
		logger.SetFormatter(formatter)
	}

	{ // Adapt to Gitea
		if hook := common.LoggerHook(ctx); hook != nil {
			logger.AddHook(hook)
		}
		if config.JobLoggerLevel != nil {
			logger.SetLevel(*config.JobLoggerLevel)
		} else {
			logger.SetLevel(logrus.TraceLevel)
		}
	}

	logger.SetFormatter(&maskedFormatter{
		Formatter: logger.Formatter,
		masker:    valueMasker(config.InsecureSecrets, config.Secrets),
	})
	rtn := logger.WithFields(logrus.Fields{
		"job":    jobName,
		"jobID":  jobID,
		"dryrun": common.Dryrun(ctx),
		"matrix": matrix,
	}).WithContext(ctx)

	return common.WithLogger(ctx, rtn)
}

func WithCompositeLogger(ctx context.Context, masks *[]string) context.Context {
	ctx = WithMasks(ctx, masks)
	return common.WithLogger(ctx, common.Logger(ctx).WithFields(logrus.Fields{}).WithContext(ctx))
}

func WithCompositeStepLogger(ctx context.Context, stepID string) context.Context {
	val := common.Logger(ctx)
	stepIDs := make([]string, 0)

	if logger, ok := val.(*logrus.Entry); ok {
		if oldStepIDs, ok := logger.Data["stepID"].([]string); ok {
			stepIDs = append(stepIDs, oldStepIDs...)
		}
	}

	stepIDs = append(stepIDs, stepID)

	return common.WithLogger(ctx, common.Logger(ctx).WithFields(logrus.Fields{
		"stepID": stepIDs,
	}).WithContext(ctx))
}

func withStepLogger(ctx context.Context, stepNumber int, stepID, stepName, stageName string) context.Context {
	rtn := common.Logger(ctx).WithFields(logrus.Fields{
		"stepNumber": stepNumber,
		"step":       stepName,
		"stepID":     []string{stepID},
		"stage":      stageName,
	})
	return common.WithLogger(ctx, rtn)
}

type entryProcessor func(entry *logrus.Entry) *logrus.Entry

// secretValueEncoders are the shapes a secret takes on its way into a log: a base64
// payload, a JSON string, or a URL component. An action that serializes a secret leaks
// it in one of these forms, which a mask of the verbatim value alone does not catch, so
// every form is masked as well. This mirrors the value encoders of GitHub's runner.
var secretValueEncoders = []func(string) string{
	func(v string) string { return base64.StdEncoding.EncodeToString([]byte(v)) },
	base64ShiftEncoder(1),
	base64ShiftEncoder(2),
	jsonStringEscape,
	jsonStringEscapeNoHTML,
	url.QueryEscape,
	url.PathEscape,
}

// minShiftedBase64Len is the shortest shifted base64 fragment worth masking. A shorter
// one carries too few bytes of the secret to identify it and would mask unrelated output.
const minShiftedBase64Len = 8

// base64ShiftEncoder returns the part of a secret's base64 form that survives when the
// secret does not start on a 3-byte boundary of the payload it is embedded in. base64
// encodes three bytes at a time, so `Authorization: Basic base64("user:token")` contains
// the base64 of the token alone only when the prefix length happens to be a multiple of
// three; at the other two alignments the encoding of the whole value differs. Encoding
// the secret behind shift filler bytes reproduces those alignments, which is what the
// Base64StringEscapeShift1/2 encoders of GitHub's runner do.
//
// The leading group (filler mixed with the secret's first bytes) and the trailing group
// (padded here, but continuing into whatever follows the secret) are dropped, leaving the
// group-aligned middle that does appear verbatim in the log.
func base64ShiftEncoder(shift int) func(string) string {
	return func(v string) string {
		buf := make([]byte, shift+len(v))
		copy(buf[shift:], v)
		encoded := base64.StdEncoding.EncodeToString(buf)
		// Keep only the aligned middle, and only when enough of it is left to be a
		// distinctive pattern rather than a fragment that matches unrelated output.
		if len(encoded) < 8+minShiftedBase64Len {
			return ""
		}
		return encoded[4 : len(encoded)-4]
	}
}

// jsonStringEscape returns v as it appears inside a JSON string, without the quotes,
// which is what `toJSON(secrets)` or any action logging a JSON body produces. Go's encoder
// escapes <, >, & (as act's own toJSON does); the non-HTML variant below covers the runtimes
// that do not. When v has none of those characters both forms are equal and deduplicated.
func jsonStringEscape(v string) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		return v
	}
	return string(encoded[1 : len(encoded)-1])
}

// jsonStringEscapeNoHTML is jsonStringEscape without HTML escaping, matching the JSON a
// JavaScript (JSON.stringify) or .NET action emits, so a secret containing < > or & is
// masked in that form too.
func jsonStringEscapeNoHTML(v string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return v
	}
	// Encode appends a newline; drop it along with the surrounding quotes.
	encoded := strings.TrimRight(buf.String(), "\n")
	return encoded[1 : len(encoded)-1]
}

func AppendSecretMasker(oldnew []string, v string) []string {
	ret := oldnew

	for l := range strings.SplitSeq(v, "\n") {
		tm := strings.TrimSpace(l)
		// formatted JSON secrets could otherwise mask {,[,],} everywhere
		if len(tm) > 1 {
			ret = append(ret, tm, "***")
			// command data reaches the log escaped, so "pass%word" also arrives as "pass%25word"
			if strings.ContainsAny(tm, "%\r\n") {
				ret = append(ret, EscapeCommandData(tm), "***")
			}
		}
	}

	// The encoded forms are derived from the whole value: a multi-line secret is
	// encoded as one string, not line by line.
	trimmed := strings.TrimSpace(v)
	if len(trimmed) <= 1 {
		return ret
	}
	for _, encode := range secretValueEncoders {
		encoded := encode(trimmed)
		// An encoding that leaves the value unchanged is already masked above.
		if encoded == trimmed || len(encoded) <= 1 || slices.Contains(ret, encoded) {
			continue
		}
		ret = append(ret, encoded, "***")
	}

	return ret
}

// valueMasker applies secrets and ::add-mask:: patterns to every log entry, including
// raw_output (command/stream) lines; there is no bypass by field.
func valueMasker(insecureSecrets bool, secrets map[string]string) entryProcessor {
	var oldnew []string
	for _, v := range secrets {
		oldnew = AppendSecretMasker(oldnew, v)
	}
	oldnew = slices.Clip(oldnew)
	defReplacer := strings.NewReplacer(oldnew...)

	// A ::add-mask:: only ever appends to the job's mask slice, so the replacer built for
	// it stays valid until the slice grows. Cache it, keyed by the slice itself and its
	// length, instead of encoding every secret and mask again for each log line.
	var (
		mu       sync.Mutex
		masksRef *[]string
		pairs    []string
		masked   int
		replacer *strings.Replacer
	)

	return func(entry *logrus.Entry) *logrus.Entry {
		if insecureSecrets {
			return entry
		}

		masks := Masks(entry.Context)

		if len(*masks) == 0 {
			entry.Message = defReplacer.Replace(entry.Message)
			return entry
		}

		mu.Lock()
		// A composite action logs through the same masker with its own mask slice, so a
		// different slice starts the cache over.
		if masksRef != masks {
			masksRef, pairs, masked, replacer = masks, oldnew, 0, nil
		}
		if replacer == nil || masked != len(*masks) {
			for _, v := range (*masks)[masked:] {
				pairs = AppendSecretMasker(pairs, v)
			}
			masked = len(*masks)
			replacer = strings.NewReplacer(pairs...)
		}
		cmasker := replacer
		mu.Unlock()

		entry.Message = cmasker.Replace(entry.Message)

		return entry
	}
}

type maskedFormatter struct {
	logrus.Formatter
	masker entryProcessor
}

func (f *maskedFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	return f.Formatter.Format(f.masker(entry))
}

type jobLogFormatter struct {
	color          int
	logPrefixJobID bool
}

func (f *jobLogFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	b := &bytes.Buffer{}

	// the web renderer decodes command data, so this local view has to as well
	if _, _, _, ok := tryParseRawActionCommand(entry.Message + "\n"); ok {
		entry.Message = UnescapeCommandData(entry.Message)
	}

	if f.isColored(entry) {
		f.printColored(b, entry)
	} else {
		f.print(b, entry)
	}

	b.WriteByte('\n')
	return b.Bytes(), nil
}

func (f *jobLogFormatter) printColored(b *bytes.Buffer, entry *logrus.Entry) {
	entry.Message = strings.TrimSuffix(entry.Message, "\n")

	var job any
	if f.logPrefixJobID {
		job = entry.Data["jobID"]
	} else {
		job = entry.Data["job"]
	}

	debugFlag := ""
	if entry.Level == logrus.DebugLevel {
		debugFlag = "[DEBUG] "
	}

	if entry.Data[rawOutputField] == true {
		if entry.Data[scriptLineCyanField] == true {
			fmt.Fprintf(b, "\x1b[%dm|\x1b[0m \x1b[36;1m%s\x1b[0m", f.color, entry.Message)
		} else {
			fmt.Fprintf(b, "\x1b[%dm|\x1b[0m %s", f.color, entry.Message)
		}
	} else if entry.Data["dryrun"] == true {
		fmt.Fprintf(b, "\x1b[1m\x1b[%dm\x1b[7m*DRYRUN*\x1b[0m \x1b[%dm[%s] \x1b[0m%s%s", gray, f.color, job, debugFlag, entry.Message)
	} else {
		fmt.Fprintf(b, "\x1b[%dm[%s] \x1b[0m%s%s", f.color, job, debugFlag, entry.Message)
	}
}

func (f *jobLogFormatter) print(b *bytes.Buffer, entry *logrus.Entry) {
	entry.Message = strings.TrimSuffix(entry.Message, "\n")

	var job any
	if f.logPrefixJobID {
		job = entry.Data["jobID"]
	} else {
		job = entry.Data["job"]
	}

	debugFlag := ""
	if entry.Level == logrus.DebugLevel {
		debugFlag = "[DEBUG] "
	}

	if entry.Data[rawOutputField] == true {
		fmt.Fprintf(b, "[%s]   | %s", job, entry.Message)
	} else if entry.Data["dryrun"] == true {
		fmt.Fprintf(b, "*DRYRUN* [%s] %s%s", job, debugFlag, entry.Message)
	} else {
		fmt.Fprintf(b, "[%s] %s%s", job, debugFlag, entry.Message)
	}
}

func (f *jobLogFormatter) isColored(entry *logrus.Entry) bool {
	isColored := checkIfTerminal(entry.Logger.Out)

	if force, ok := os.LookupEnv("CLICOLOR_FORCE"); ok && force != "0" {
		isColored = true
	} else if ok && force == "0" {
		isColored = false
	} else if os.Getenv("CLICOLOR") == "0" {
		isColored = false
	}

	return isColored
}

func checkIfTerminal(w io.Writer) bool {
	switch v := w.(type) {
	case *os.File:
		return term.IsTerminal(int(v.Fd()))
	default:
		return false
	}
}
