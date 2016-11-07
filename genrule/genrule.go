// Copyright 2015 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package genrule

import (
	"fmt"
	"strings"

	"github.com/google/blueprint"

	"android/soong/android"
)

func init() {
	android.RegisterModuleType("gensrcs", GenSrcsFactory)
	android.RegisterModuleType("genrule", GenRuleFactory)
}

var (
	pctx = android.NewPackageContext("android/soong/genrule")
)

type SourceFileGenerator interface {
	GeneratedSourceFiles() android.Paths
	GeneratedHeaderDir() android.Path
}

type HostToolProvider interface {
	HostToolPath() android.OptionalPath
}

type generatorProperties struct {
	// command to run on one or more input files.  Available variables for substitution:
	// $(location): the path to the first entry in tools or tool_files
	// $(location <label>): the path to the tool or tool_file with name <label>
	// $(in): one or more input files
	// $(out): a single output file
	// $(genDir): the sandbox directory for this tool; contains $(out)
	// $$: a literal $
	//
	// DO NOT directly reference paths to files in the source tree, or the
	// command will be missing proper dependencies to re-run if the files
	// change.
	Cmd string

	// name of the modules (if any) that produces the host executable.   Leave empty for
	// prebuilts or scripts that do not need a module to build them.
	Tools []string

	// Local file that is used as the tool
	Tool_files []string
}

type generator struct {
	android.ModuleBase

	properties generatorProperties

	tasks taskFunc

	deps android.Paths
	rule blueprint.Rule

	genPath android.Path

	outputFiles android.Paths
}

type taskFunc func(ctx android.ModuleContext) []generateTask

type generateTask struct {
	in  android.Paths
	out android.WritablePaths
}

func (g *generator) GeneratedSourceFiles() android.Paths {
	return g.outputFiles
}

func (g *generator) GeneratedHeaderDir() android.Path {
	return g.genPath
}

func (g *generator) DepsMutator(ctx android.BottomUpMutatorContext) {
	if g, ok := ctx.Module().(*generator); ok {
		if len(g.properties.Tools) > 0 {
			ctx.AddFarVariationDependencies([]blueprint.Variation{
				{"arch", ctx.AConfig().BuildOsVariant},
			}, nil, g.properties.Tools...)
		}
	}
}

func (g *generator) GenerateAndroidBuildActions(ctx android.ModuleContext) {
	if len(g.properties.Tools) == 0 && len(g.properties.Tool_files) == 0 {
		ctx.ModuleErrorf("at least one `tools` or `tool_files` is required")
		return
	}

	g.genPath = android.PathForModuleGen(ctx, "")

	tools := map[string]android.Path{}

	if len(g.properties.Tools) > 0 {
		ctx.VisitDirectDeps(func(module blueprint.Module) {
			if t, ok := module.(HostToolProvider); ok {
				p := t.HostToolPath()
				if p.Valid() {
					g.deps = append(g.deps, p.Path())
					tool := ctx.OtherModuleName(module)
					if _, exists := tools[tool]; !exists {
						tools[tool] = p.Path()
					} else {
						ctx.ModuleErrorf("multiple tools for %q, %q and %q", tool, tools[tool], p.Path().String())
					}
				} else {
					ctx.ModuleErrorf("host tool %q missing output file", ctx.OtherModuleName(module))
				}
			} else {
				ctx.ModuleErrorf("unknown dependency %q", ctx.OtherModuleName(module))
			}
		})
	}

	for _, tool := range g.properties.Tool_files {
		toolPath := android.PathForModuleSrc(ctx, tool)
		g.deps = append(g.deps, toolPath)
		if _, exists := tools[tool]; !exists {
			tools[tool] = toolPath
		} else {
			ctx.ModuleErrorf("multiple tools for %q, %q and %q", tool, tools[tool], toolPath.String())
		}
	}

	cmd, err := android.Expand(g.properties.Cmd, func(name string) (string, error) {
		switch name {
		case "location":
			if len(g.properties.Tools) > 0 {
				return tools[g.properties.Tools[0]].String(), nil
			} else {
				return tools[g.properties.Tool_files[0]].String(), nil
			}
		case "in":
			return "${in}", nil
		case "out":
			return "${out}", nil
		case "genDir":
			return g.genPath.String(), nil
		default:
			if strings.HasPrefix(name, "location ") {
				label := strings.TrimSpace(strings.TrimPrefix(name, "location "))
				if tool, ok := tools[label]; ok {
					return tool.String(), nil
				} else {
					return "", fmt.Errorf("unknown location label %q", label)
				}
			}
			return "", fmt.Errorf("unknown variable '$(%s)'", name)
		}
	})

	if err != nil {
		ctx.PropertyErrorf("cmd", "%s", err.Error())
	}

	g.rule = ctx.Rule(pctx, "generator", blueprint.RuleParams{
		Command: cmd,
	})

	for _, task := range g.tasks(ctx) {
		g.generateSourceFile(ctx, task)
	}
}

func (g *generator) generateSourceFile(ctx android.ModuleContext, task generateTask) {
	ctx.ModuleBuild(pctx, android.ModuleBuildParams{
		Rule:      g.rule,
		Outputs:   task.out,
		Inputs:    task.in,
		Implicits: g.deps,
	})

	for _, outputFile := range task.out {
		g.outputFiles = append(g.outputFiles, outputFile)
	}
}

func generatorFactory(tasks taskFunc, props ...interface{}) (blueprint.Module, []interface{}) {
	module := &generator{
		tasks: tasks,
	}

	props = append(props, &module.properties)

	return android.InitAndroidModule(module, props...)
}

func GenSrcsFactory() (blueprint.Module, []interface{}) {
	properties := &genSrcsProperties{}

	tasks := func(ctx android.ModuleContext) []generateTask {
		srcFiles := ctx.ExpandSources(properties.Srcs, nil)
		tasks := make([]generateTask, 0, len(srcFiles))
		for _, in := range srcFiles {
			tasks = append(tasks, generateTask{
				in:  android.Paths{in},
				out: android.WritablePaths{android.GenPathWithExt(ctx, "", in, properties.Output_extension)},
			})
		}
		return tasks
	}

	return generatorFactory(tasks, properties)
}

type genSrcsProperties struct {
	// list of input files
	Srcs []string

	// extension that will be substituted for each output file
	Output_extension string
}

func GenRuleFactory() (blueprint.Module, []interface{}) {
	properties := &genRuleProperties{}

	tasks := func(ctx android.ModuleContext) []generateTask {
		outs := make(android.WritablePaths, len(properties.Out))
		for i, out := range properties.Out {
			outs[i] = android.PathForModuleGen(ctx, out)
		}
		return []generateTask{
			{
				in:  ctx.ExpandSources(properties.Srcs, nil),
				out: outs,
			},
		}
	}

	return generatorFactory(tasks, properties)
}

type genRuleProperties struct {
	// list of input files
	Srcs []string

	// names of the output files that will be generated
	Out []string
}
