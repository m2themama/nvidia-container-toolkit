/**
# Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
**/

package main

import (
	"context"
	"os"

	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvidia-container-toolkit/internal/info"

	cli "github.com/urfave/cli/v3"

	"github.com/NVIDIA/nvidia-container-toolkit/cmd/nvidia-cdi-hook/commands"
)

// options defines the options that can be set for the CLI through config files,
// environment variables, or command line flags
type options struct {
	// Debug indicates whether the CLI is started in "debug" mode
	Debug bool
	// Quiet indicates whether the CLI is started in "quiet" mode
	Quiet bool
}

func main() {
	logger := logrus.New()

	// Create a options struct to hold the parsed environment variables or command line flags
	opts := options{}

	// Create the top-level CLI
	c := cli.Command{
		Name:    "NVIDIA CDI Hook",
		Usage:   "Command to structure files for usage inside a container, called as hooks from a container runtime, defined in a CDI yaml file",
		Version: info.GetVersionString(),
		// Set log-level for all subcommands
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			logLevel := logrus.InfoLevel
			if opts.Debug {
				logLevel = logrus.DebugLevel
			}
			if opts.Quiet {
				logLevel = logrus.ErrorLevel
			}
			logger.SetLevel(logLevel)
			return ctx, nil
		},
		// We set the default action for the `nvidia-cdi-hook` command to issue a
		// warning and exit with no error.
		// This means that if an unsupported hook is run, a container will not fail
		// to launch. An unsupported hook could be the result of a CDI specification
		// referring to a new hook that is not yet supported by an older NVIDIA
		// Container Toolkit version or a hook that has been removed in newer
		// version.
		Action: func(ctx context.Context, cmd *cli.Command) error {
			commands.IssueUnsupportedHookWarning(logger, cmd)
			return nil
		},
		// Define the subcommands
		Commands: commands.New(logger),
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:        "debug",
				Aliases:     []string{"d"},
				Usage:       "Enable debug-level logging",
				Destination: &opts.Debug,
				// TODO: Support for NVIDIA_CDI_DEBUG is deprecated and NVIDIA_CTK_DEBUG should be used instead.
				Sources: cli.EnvVars("NVIDIA_CTK_DEBUG", "NVIDIA_CDI_DEBUG"),
			},
			&cli.BoolFlag{
				Name:        "quiet",
				Usage:       "Suppress all output except for errors; overrides --debug",
				Destination: &opts.Quiet,
				// TODO: Support for NVIDIA_CDI_QUIET is deprecated and NVIDIA_CTK_QUIET should be used instead.
				Sources: cli.EnvVars("NVIDIA_CTK_QUIET", "NVIDIA_CDI_QUIET"),
			},
		},
	}

	// Run the CLI
	err := c.Run(context.Background(), os.Args)
	if err != nil {
		logger.Errorf("%v", err)
		os.Exit(1)
	}
}
