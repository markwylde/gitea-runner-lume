// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"context"
	"strings"
	"testing"

	"gitea.com/gitea/runner/act/exprparser"
	"gitea.com/gitea/runner/act/model"

	assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yaml "go.yaml.in/yaml/v4"
)

func createRunContext(t *testing.T) *RunContext {
	var yml yaml.Node
	err := yml.Encode(map[string][]any{
		"os":  {"Linux", "Windows"},
		"foo": {"bar", "baz"},
	})
	assert.NoError(t, err)

	return &RunContext{
		Config: &Config{
			Workdir: ".",
			Secrets: map[string]string{
				"CASE_INSENSITIVE_SECRET": "value",
			},
			Vars: map[string]string{
				"CASE_INSENSITIVE_VAR": "value",
			},
		},
		Env: map[string]string{
			"key": "value",
		},
		Run: &model.Run{
			JobID: "job1",
			Workflow: &model.Workflow{
				Name: "test-workflow",
				Jobs: map[string]*model.Job{
					"job1": {
						Strategy: &model.Strategy{
							RawMatrix: yml,
						},
					},
				},
			},
		},
		Matrix: map[string]any{
			"os":  "Linux",
			"foo": "bar",
		},
		StepResults: map[string]*model.StepResult{
			"idwithnothing": {
				Conclusion: model.StepStatusSuccess,
				Outcome:    model.StepStatusFailure,
				Outputs: map[string]string{
					"foowithnothing": "barwithnothing",
				},
			},
			"id-with-hyphens": {
				Conclusion: model.StepStatusSuccess,
				Outcome:    model.StepStatusFailure,
				Outputs: map[string]string{
					"foo-with-hyphens": "bar-with-hyphens",
				},
			},
			"id_with_underscores": {
				Conclusion: model.StepStatusSuccess,
				Outcome:    model.StepStatusFailure,
				Outputs: map[string]string{
					"foo_with_underscores": "bar_with_underscores",
				},
			},
		},
	}
}

func TestEvaluateRunContext(t *testing.T) {
	rc := createRunContext(t)
	ee := rc.NewExpressionEvaluator(context.Background())

	tables := []struct {
		in      string
		out     any
		errMesg string
	}{
		{" 1 ", 1, ""},
		// {"1 + 3", "4", ""},
		// {"(1 + 3) * -2", "-8", ""},
		{"'my text'", "my text", ""},
		{"contains('my text', 'te')", true, ""},
		{"contains('my TEXT', 'te')", true, ""},
		{"contains(fromJSON('[\"my text\"]'), 'te')", false, ""},
		{"contains(fromJSON('[\"foo\",\"bar\"]'), 'bar')", true, ""},
		{"startsWith('hello world', 'He')", true, ""},
		{"endsWith('hello world', 'ld')", true, ""},
		{"format('0:{0} 2:{2} 1:{1}', 'zero', 'one', 'two')", "0:zero 2:two 1:one", ""},
		{"join(fromJSON('[\"hello\"]'),'octocat')", "hello", ""},
		{"join(fromJSON('[\"hello\",\"mona\",\"the\"]'),'octocat')", "hellooctocatmonaoctocatthe", ""},
		{"join('hello','mona')", "hello", ""},
		{"toJSON(env)", "{\n  \"ACT\": \"true\",\n  \"ACT_SKIP_CHECKOUT\": \"true\",\n  \"key\": \"value\"\n}", ""},
		{"toJson(env)", "{\n  \"ACT\": \"true\",\n  \"ACT_SKIP_CHECKOUT\": \"true\",\n  \"key\": \"value\"\n}", ""},
		{"(fromJSON('{\"foo\":\"bar\"}')).foo", "bar", ""},
		{"(fromJson('{\"foo\":\"bar\"}')).foo", "bar", ""},
		{"(fromJson('[\"foo\",\"bar\"]'))[1]", "bar", ""},
		// github does return an empty string for non-existent files
		{"hashFiles('**/non-extant-files')", "", ""},
		{"hashFiles('**/non-extant-files', '**/more-non-extant-files')", "", ""},
		{"hashFiles('**/non.extant.files')", "", ""},
		{"hashFiles('**/non''extant''files')", "", ""},
		{"success()", true, ""},
		{"failure()", false, ""},
		{"always()", true, ""},
		{"cancelled()", false, ""},
		{"github.workflow", "test-workflow", ""},
		{"github.actor", "nektos/act", ""},
		{"github.run_id", "1", ""},
		{"github.run_number", "1", ""},
		{"job.status", "success", ""},
		{"matrix.os", "Linux", ""},
		{"matrix.foo", "bar", ""},
		{"env.key", "value", ""},
		{"secrets.CASE_INSENSITIVE_SECRET", "value", ""},
		{"secrets.case_insensitive_secret", "value", ""},
		{"vars.CASE_INSENSITIVE_VAR", "value", ""},
		{"vars.case_insensitive_var", "value", ""},
		{"format('{{0}}', 'test')", "{0}", ""},
		{"format('{{{0}}}', 'test')", "{test}", ""},
		{"format('}}')", "}", ""},
		{"format('echo Hello {0} ${{Test}}', 'World')", "echo Hello World ${Test}", ""},
		{"format('echo Hello {0} ${{Test}}', github.undefined_property)", "echo Hello  ${Test}", ""},
		{"format('echo Hello {0}{1} ${{Te{0}st}}', github.undefined_property, 'World')", "echo Hello World ${Test}", ""},
		{"format('{0}', '{1}', 'World')", "{1}", ""},
		{"format('{{{0}', '{1}', 'World')", "{{1}", ""},
	}

	for _, table := range tables {
		t.Run(table.in, func(t *testing.T) {
			assertObject := assert.New(t)
			out, err := ee.evaluate(context.Background(), table.in, exprparser.DefaultStatusCheckNone)
			if table.errMesg == "" {
				assertObject.NoError(err, table.in) //nolint:testifylint // pre-existing issue from nektos/act
				assertObject.Equal(table.out, out, table.in)
			} else {
				assertObject.Error(err, table.in) //nolint:testifylint // pre-existing issue from nektos/act
				assertObject.Equal(table.errMesg, err.Error(), table.in)
			}
		})
	}
}

func TestEvaluateStep(t *testing.T) {
	rc := createRunContext(t)
	step := &stepRun{
		RunContext: rc,
	}

	ee := rc.NewStepExpressionEvaluator(context.Background(), step)

	tables := []struct {
		in      string
		out     any
		errMesg string
	}{
		{"steps.idwithnothing.conclusion", model.StepStatusSuccess.String(), ""},
		{"steps.idwithnothing.outcome", model.StepStatusFailure.String(), ""},
		{"steps.idwithnothing.outputs.foowithnothing", "barwithnothing", ""},
		{"steps.id-with-hyphens.conclusion", model.StepStatusSuccess.String(), ""},
		{"steps.id-with-hyphens.outcome", model.StepStatusFailure.String(), ""},
		{"steps.id-with-hyphens.outputs.foo-with-hyphens", "bar-with-hyphens", ""},
		{"steps.id_with_underscores.conclusion", model.StepStatusSuccess.String(), ""},
		{"steps.id_with_underscores.outcome", model.StepStatusFailure.String(), ""},
		{"steps.id_with_underscores.outputs.foo_with_underscores", "bar_with_underscores", ""},
	}

	for _, table := range tables {
		t.Run(table.in, func(t *testing.T) {
			assertObject := assert.New(t)
			out, err := ee.evaluate(context.Background(), table.in, exprparser.DefaultStatusCheckNone)
			if table.errMesg == "" {
				assertObject.NoError(err, table.in) //nolint:testifylint // pre-existing issue from nektos/act
				assertObject.Equal(table.out, out, table.in)
			} else {
				assertObject.Error(err, table.in) //nolint:testifylint // pre-existing issue from nektos/act
				assertObject.Equal(table.errMesg, err.Error(), table.in)
			}
		})
	}
}

func TestInterpolate(t *testing.T) {
	rc := &RunContext{
		Config: &Config{
			Workdir: ".",
			Secrets: map[string]string{
				"CASE_INSENSITIVE_SECRET": "value",
			},
			Vars: map[string]string{
				"CASE_INSENSITIVE_VAR": "value",
			},
		},
		Env: map[string]string{
			"KEYWITHNOTHING":       "valuewithnothing",
			"KEY-WITH-HYPHENS":     "value-with-hyphens",
			"KEY_WITH_UNDERSCORES": "value_with_underscores",
			"SOMETHING_TRUE":       "true",
			"SOMETHING_FALSE":      "false",
		},
		Run: &model.Run{
			JobID: "job1",
			Workflow: &model.Workflow{
				Name: "test-workflow",
				Jobs: map[string]*model.Job{
					"job1": {},
				},
			},
		},
	}
	ee := rc.NewExpressionEvaluator(context.Background())
	tables := []struct {
		in  string
		out string
	}{
		{" text ", " text "},
		{" $text ", " $text "},
		{" ${text} ", " ${text} "},
		{" ${{          1                         }} to ${{2}} ", " 1 to 2 "},
		{" ${{  (true || false)  }} to ${{2}} ", " true to 2 "},
		{" ${{  (false   ||  '}}'  )    }} to ${{2}} ", " }} to 2 "},
		{" ${{ env.KEYWITHNOTHING }} ", " valuewithnothing "},
		{" ${{ env.KEY-WITH-HYPHENS }} ", " value-with-hyphens "},
		{" ${{ env.KEY_WITH_UNDERSCORES }} ", " value_with_underscores "},
		{"${{ secrets.CASE_INSENSITIVE_SECRET }}", "value"},
		{"${{ secrets.case_insensitive_secret }}", "value"},
		{"${{ vars.CASE_INSENSITIVE_VAR }}", "value"},
		{"${{ vars.case_insensitive_var }}", "value"},
		{"${{ env.UNKNOWN }}", ""},
		{"${{ env.SOMETHING_TRUE }}", "true"},
		{"${{ env.SOMETHING_FALSE }}", "false"},
		{"${{ !env.SOMETHING_TRUE }}", "false"},
		{"${{ !env.SOMETHING_FALSE }}", "false"},
		{"${{ !env.SOMETHING_TRUE && true }}", "false"},
		{"${{ !env.SOMETHING_FALSE && true }}", "false"},
		{"${{ env.SOMETHING_TRUE && true }}", "true"},
		{"${{ env.SOMETHING_FALSE && true }}", "true"},
		{"${{ !env.SOMETHING_TRUE || true }}", "true"},
		{"${{ !env.SOMETHING_FALSE || true }}", "true"},
		{"${{ !env.SOMETHING_TRUE && false }}", "false"},
		{"${{ !env.SOMETHING_FALSE && false }}", "false"},
		{"${{ !env.SOMETHING_TRUE || false }}", "false"},
		{"${{ !env.SOMETHING_FALSE || false }}", "false"},
		{"${{ env.SOMETHING_TRUE || false }}", "true"},
		{"${{ env.SOMETHING_FALSE || false }}", "false"},
		{"${{ env.SOMETHING_FALSE }} && ${{ env.SOMETHING_TRUE }}", "false && true"},
		{"${{ fromJSON('{}') < 2 }}", "false"},
		{"${{ 1 }}", "1"},
		{"${{ 1.0 }}", "1"},
		{"${{ null }}", ""},
		{"${{ fromJSON('[1,2]') }}", "Array"},
		{"${{ fromJSON('{\"a\":1}') }}", "Object"},
		// a malformed part must not restructure its neighbours, and it interpolates to nothing
		{"${{ 1) && (2 }}", ""},
		{"run ${{ 1) && (2 }} now", ""},
		{"${{ 1", "${{ 1"},
	}

	for _, table := range tables {
		t.Run("interpolate", func(t *testing.T) {
			assertObject := assert.New(t)
			out := ee.Interpolate(context.Background(), table.in)
			assertObject.Equal(table.out, out, table.in)
		})
	}
}

func TestSplitSubExpressions(t *testing.T) {
	expr := func(text string) exprPart { return exprPart{text: text, isExpr: true} }
	literal := func(text string) exprPart { return exprPart{text: text} }

	for _, tt := range []struct {
		in   string
		want []exprPart
	}{
		{"Hello World", []exprPart{literal("Hello World")}},
		{"${{ true }}", []exprPart{expr("true")}},
		{"${{ true }} ${{ false }}", []exprPart{expr("true"), literal(" "), expr("false")}},
		{"Hello ${{ 'World' }}", []exprPart{literal("Hello "), expr("'World'")}},
		// a quote toggles string state, so a `}}` inside a string does not end the expression
		{"${{ '}}' }}", []exprPart{expr("'}}'")}},
		{"${{ '''}}''' }}", []exprPart{expr("'''}}'''")}},
		{"${{ '''' }}", []exprPart{expr("''''")}},
		{`${{ fromJSON('"}}"') }}`, []exprPart{expr(`fromJSON('"}}"')`)}},
		{`${{ fromJSON('"\"}}\""') }}`, []exprPart{expr(`fromJSON('"\"}}\""')`)}},
		{`${{ fromJSON('"''}}"') }}`, []exprPart{expr(`fromJSON('"''}}"')`)}},
		// without a complete literal the value stays text, as GitHub's template reader leaves it
		{"${{ 1", []exprPart{literal("${{ 1")}},
		// a malformed part stays one part, so it cannot restructure its neighbours
		{"${{ 1) && (2 }}", []exprPart{expr("1) && (2")}},
	} {
		got, err := splitSubExpressions(tt.in)
		require.NoError(t, err, tt.in)
		assert.Equal(t, tt.want, got, tt.in)
	}

	for _, in := range []string{"${{ 'a' }} ${{ b", "${{ 'a }}"} {
		_, err := splitSubExpressions(in)
		assert.ErrorContains(t, err, "unclosed expression", in)
	}
}

func TestGetEvaluatorInputsBoolean(t *testing.T) {
	workflows := map[string]string{
		"workflow_call": `
on:
  workflow_call:
    inputs:
      flag:
        type: boolean
        default: true
      name:
        type: string
        default: gitea
`,
		"workflow_dispatch": `
on:
  workflow_dispatch:
    inputs:
      flag:
        type: boolean
        default: true
      name:
        type: string
        default: gitea
`,
	}

	tables := []struct {
		name  string
		event map[string]any
		flag  any
	}{
		{
			// Gitea >= 1.27 resolves the inputs server-side and sends native JSON types
			name:  "native bool true",
			event: map[string]any{"inputs": map[string]any{"flag": true}},
			flag:  true,
		},
		{
			name:  "native bool false",
			event: map[string]any{"inputs": map[string]any{"flag": false}},
			flag:  false,
		},
		{
			name:  "string true",
			event: map[string]any{"inputs": map[string]any{"flag": "true"}},
			flag:  true,
		},
		{
			name:  "string false",
			event: map[string]any{"inputs": map[string]any{"flag": "false"}},
			flag:  false,
		},
		{
			name:  "default is used when the event carries no inputs",
			event: map[string]any{},
			flag:  true,
		},
	}

	for eventName, workflow := range workflows {
		for _, table := range tables {
			t.Run(eventName+"/"+table.name, func(t *testing.T) {
				wf, err := model.ReadWorkflow(strings.NewReader(workflow))
				require.NoError(t, err)

				rc := &RunContext{
					Config: &Config{Workdir: "."},
					Run:    &model.Run{JobID: "job1", Workflow: wf},
				}
				ghc := &model.GithubContext{EventName: eventName, Event: table.event}

				inputs := getEvaluatorInputs(context.Background(), rc, nil, ghc)
				assert.Equal(t, table.flag, inputs["flag"])
				assert.Equal(t, "gitea", inputs["name"])
			})
		}
	}
}
