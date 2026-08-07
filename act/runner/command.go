// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"gitea.com/gitea/runner/act/common"
)

var commandPatternGA *regexp.Regexp

var commandPatternADO *regexp.Regexp

func init() {
	commandPatternGA = regexp.MustCompile("^::([^ ]+)( (.+))?::([^\r\n]*)[\r\n]+$")
	commandPatternADO = regexp.MustCompile("^##\\[([^ ]+)( (.+))?]([^\r\n]*)[\r\n]+$")
}

func tryParseRawActionCommand(line string) (command string, kvPairs map[string]string, arg string, ok bool) {
	if m := commandPatternGA.FindStringSubmatch(line); m != nil {
		command = m[1]
		kvPairs = parseKeyValuePairs(m[3], ",")
		arg = m[4]
		ok = true
	} else if m := commandPatternADO.FindStringSubmatch(line); m != nil {
		command = m[1]
		kvPairs = parseKeyValuePairs(m[3], ";")
		arg = m[4]
		ok = true
	}
	return command, kvPairs, arg, ok
}

func (rc *RunContext) commandHandler(ctx context.Context) common.LineHandler {
	logger := common.Logger(ctx)
	resumeCommand := ""
	return func(line string) bool {
		command, kvPairs, arg, ok := tryParseRawActionCommand(line)
		if !ok {
			return true
		}

		if resumeCommand != "" {
			// There should not be any emojis in the log output for Gitea.
			// Return true (not false) so the line still reaches the raw_output
			// log handler; otherwise everything between ::stop-commands:: and
			// its end token is silently dropped from the step log.
			logger.Infof("%s", line)
			// Resumed here rather than from the switch, because the end token is arbitrary
			// and a token naming a real command would otherwise never resume.
			if command == resumeCommand {
				resumeCommand = ""
			}
			return true
		}
		arg = UnescapeCommandData(arg)
		kvPairs = unescapeKvPairs(kvPairs)
		if (command == "set-env" || command == "add-path") && rc.refuseUnsecureCommand(ctx, command) {
			return true
		}
		switch command {
		case "set-env":
			rc.setEnv(ctx, kvPairs, arg)
		case "set-output":
			rc.setOutput(ctx, kvPairs, arg)
		case "add-path":
			rc.addPath(ctx, arg)
		case "add-mask":
			rc.AddMask(arg)
			logger.Infof("%s", "***")
			// The raw line is still forwarded, carrying the secret: that is how the reporter
			// learns the mask, and it drops the row rather than writing it out.
		case "stop-commands":
			resumeCommand = arg
			logger.Infof("%s", line)
		case "save-state":
			logger.Infof("%s", line)
			rc.saveState(ctx, kvPairs, arg)
		default:
			// ::debug::, ::error::, ::warning::, ::add-matcher:: and anything unrecognised are
			// passed through for the reporter and Gitea's web UI to render.
			logger.Infof("%s", line)
		}

		// return true to let gitea's logger handle these special outputs also
		return true
	}
}

const allowUnsecureCommandsVar = "ACTIONS_ALLOW_UNSECURE_COMMANDS"

// refuseUnsecureCommand reports whether a deprecated ::set-env:: or ::add-path:: command must
// not run, recording the error that fails the step. GitHub disabled both because a step that
// echoes untrusted content can use them to set NODE_OPTIONS or PATH for every later step.
func (rc *RunContext) refuseUnsecureCommand(ctx context.Context, command string) bool {
	if rc.allowUnsecureCommandsOptIn() {
		return false
	}

	// The step executor logs the failure itself, so keep this line's wording distinct.
	common.Logger(ctx).WithField(rawOutputField, true).Errorf("##[error]%s", EscapeCommandData(fmt.Sprintf(
		"The `%s` command is disabled: it can set the environment of every later step from untrusted output. "+
			"Write to $GITHUB_ENV or $GITHUB_PATH instead, or set ACTIONS_ALLOW_UNSECURE_COMMANDS to allow it",
		command)))

	rc.unsecureCommandMu.Lock()
	defer rc.unsecureCommandMu.Unlock()
	if rc.unsecureCommandErr == nil {
		rc.unsecureCommandErr = fmt.Errorf("the `%s` workflow command is disabled", command)
	}
	return true
}

// allowUnsecureCommandsOptIn reports whether the workflow itself asked for the deprecated
// commands, from any env scope, as it can on GitHub.
func (rc *RunContext) allowUnsecureCommandsOptIn() bool {
	return isTruthyEnv(rc.currentStepEnv()[allowUnsecureCommandsVar]) ||
		isTruthyEnv(rc.Env[allowUnsecureCommandsVar]) ||
		isTruthyEnv(rc.GlobalEnv[allowUnsecureCommandsVar])
}

// isTruthyEnv mirrors GitHub's bool.TryParse: only "true", in any casing.
func isTruthyEnv(v string) bool {
	return strings.EqualFold(strings.TrimSpace(v), "true")
}

// takeUnsecureCommandError returns and clears the error left by a refused command.
func (rc *RunContext) takeUnsecureCommandError() error {
	rc.unsecureCommandMu.Lock()
	defer rc.unsecureCommandMu.Unlock()
	err := rc.unsecureCommandErr
	rc.unsecureCommandErr = nil
	return err
}

func (rc *RunContext) setEnv(ctx context.Context, kvPairs map[string]string, arg string) {
	name := kvPairs["name"]
	common.Logger(ctx).Infof("::set-env:: %s=%s", name, arg)
	if rc.Env == nil {
		rc.Env = make(map[string]string)
	}
	if rc.GlobalEnv == nil {
		rc.GlobalEnv = map[string]string{}
	}
	newenv := map[string]string{
		name: arg,
	}
	mergeIntoMap := mergeIntoMapCaseSensitive
	if rc.JobContainer != nil && rc.JobContainer.IsEnvironmentCaseInsensitive() {
		mergeIntoMap = mergeIntoMapCaseInsensitive
	}
	mergeIntoMap(rc.Env, newenv)
	mergeIntoMap(rc.GlobalEnv, newenv)
}

func (rc *RunContext) setOutput(ctx context.Context, kvPairs map[string]string, arg string) {
	logger := common.Logger(ctx)
	stepID := rc.CurrentStep
	outputName := kvPairs["name"]
	if outputMapping, ok := rc.OutputMappings[MappableOutput{StepID: stepID, OutputName: outputName}]; ok {
		stepID = outputMapping.StepID
		outputName = outputMapping.OutputName
	}

	result, ok := rc.StepResults[stepID]
	if !ok {
		logger.Infof("No outputs registered for step '%s'", stepID)
		return
	}

	logger.Infof("::set-output:: %s=%s", outputName, arg)
	result.Outputs[outputName] = arg
}

func (rc *RunContext) addPath(ctx context.Context, arg string) {
	common.Logger(ctx).Infof("::add-path:: %s", arg)
	extraPath := []string{arg}
	for _, v := range rc.ExtraPath {
		if v != arg {
			extraPath = append(extraPath, v)
		}
	}
	rc.ExtraPath = extraPath
}

func parseKeyValuePairs(kvPairs, separator string) map[string]string {
	rtn := make(map[string]string)
	kvPairList := strings.SplitSeq(kvPairs, separator)
	for kvPair := range kvPairList {
		kv := strings.Split(kvPair, "=")
		if len(kv) == 2 {
			rtn[kv[0]] = kv[1]
		}
	}
	return rtn
}

// A Replacer never rescans what it wrote, so "%250A" stays a literal "%0A".
var (
	commandDataEscaper       = strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")
	commandDataUnescaper     = strings.NewReplacer("%25", "%", "%0D", "\r", "%0A", "\n")
	commandPropertyUnescaper = strings.NewReplacer("%25", "%", "%0D", "\r", "%0A", "\n", "%3A", ":", "%2C", ",")
)

// EscapeCommandData encodes the data part of a "::cmd::" or "##[cmd]" line the runner writes itself,
// so the log renderer decodes it back. Lines forwarded from step output are already escaped.
func EscapeCommandData(arg string) string {
	return commandDataEscaper.Replace(arg)
}

func UnescapeCommandData(arg string) string {
	return commandDataUnescaper.Replace(arg)
}

func unescapeCommandProperty(arg string) string {
	return commandPropertyUnescaper.Replace(arg)
}

func unescapeKvPairs(kvPairs map[string]string) map[string]string {
	for k, v := range kvPairs {
		kvPairs[k] = unescapeCommandProperty(v)
	}
	return kvPairs
}

func (rc *RunContext) saveState(_ context.Context, kvPairs map[string]string, arg string) {
	stepID := rc.CurrentStep
	if stepID != "" {
		if rc.IntraActionState == nil {
			rc.IntraActionState = map[string]map[string]string{}
		}
		state, ok := rc.IntraActionState[stepID]
		if !ok {
			state = map[string]string{}
			rc.IntraActionState[stepID] = state
		}
		state[kvPairs["name"]] = arg
	}
}
