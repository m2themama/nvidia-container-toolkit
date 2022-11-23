/**
# Copyright (c) 2022, NVIDIA CORPORATION.  All rights reserved.
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

package generate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nvidia-container-toolkit/internal/discover"
	"github.com/NVIDIA/nvidia-container-toolkit/internal/edits"
	"github.com/container-orchestrated-devices/container-device-interface/pkg/cdi"
	specs "github.com/container-orchestrated-devices/container-device-interface/specs-go"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"gitlab.com/nvidia/cloud-native/go-nvlib/pkg/nvlib/device"
	"gitlab.com/nvidia/cloud-native/go-nvlib/pkg/nvml"
	"sigs.k8s.io/yaml"
)

const (
	nvidiaCTKExecutable      = "nvidia-ctk"
	nvidiaCTKDefaultFilePath = "/usr/bin/" + nvidiaCTKExecutable

	formatJSON = "json"
	formatYAML = "yaml"
)

type command struct {
	logger *logrus.Logger
}

type config struct {
	output string
	format string
}

// NewCommand constructs a generate-cdi command with the specified logger
func NewCommand(logger *logrus.Logger) *cli.Command {
	c := command{
		logger: logger,
	}
	return c.build()
}

// build creates the CLI command
func (m command) build() *cli.Command {
	cfg := config{}

	// Create the 'generate-cdi' command
	c := cli.Command{
		Name:  "generate",
		Usage: "Generate CDI specifications for use with CDI-enabled runtimes",
		Before: func(c *cli.Context) error {
			return m.validateFlags(c, &cfg)
		},
		Action: func(c *cli.Context) error {
			return m.run(c, &cfg)
		},
	}

	c.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:        "output",
			Usage:       "Specify the file to output the generated CDI specification to. If this is '' the specification is output to STDOUT",
			Destination: &cfg.output,
		},
		&cli.StringFlag{
			Name:        "format",
			Usage:       "The output format for the generated spec [json | yaml]. This overrides the format defined by the output file extension (if specified).",
			Value:       formatYAML,
			Destination: &cfg.format,
		},
	}

	return &c
}

func (m command) validateFlags(r *cli.Context, cfg *config) error {
	cfg.format = strings.ToLower(cfg.format)
	switch cfg.format {
	case formatJSON:
	case formatYAML:
	default:
		return fmt.Errorf("invalid output format: %v", cfg.format)
	}

	return nil
}

func (m command) run(c *cli.Context, cfg *config) error {
	spec, err := m.generateSpec()
	if err != nil {
		return fmt.Errorf("failed to generate CDI spec: %v", err)
	}

	var outputTo io.Writer
	if cfg.output == "" {
		outputTo = os.Stdout
	} else {
		err := createParentDirsIfRequired(cfg.output)
		if err != nil {
			return fmt.Errorf("failed to create parent folders for output file: %v", err)
		}
		outputFile, err := os.Create(cfg.output)
		if err != nil {
			return fmt.Errorf("failed to create output file: %v", err)
		}
		defer outputFile.Close()
		outputTo = outputFile
	}

	if outputFileFormat := formatFromFilename(cfg.output); outputFileFormat != "" {
		m.logger.Debugf("Inferred output format as %q from output file name", outputFileFormat)
		if !c.IsSet("format") {
			cfg.format = outputFileFormat
		} else if outputFileFormat != cfg.format {
			m.logger.Warningf("Requested output format %q does not match format implied by output file name: %q", cfg.format, outputFileFormat)
		}
	}

	data, err := yaml.Marshal(spec)
	if err != nil {
		return fmt.Errorf("failed to marshal CDI spec: %v", err)
	}

	if strings.ToLower(cfg.format) == formatJSON {
		data, err = yaml.YAMLToJSONStrict(data)
		if err != nil {
			return fmt.Errorf("failed to convert CDI spec from YAML to JSON: %v", err)
		}
	}

	err = writeToOutput(cfg.format, data, outputTo)
	if err != nil {
		return fmt.Errorf("failed to write output: %v", err)
	}

	return nil
}

func formatFromFilename(filename string) string {
	ext := filepath.Ext(filename)
	switch strings.ToLower(ext) {
	case ".json":
		return formatJSON
	case ".yaml":
		return formatYAML
	case ".yml":
		return formatYAML
	}

	return ""
}

func writeToOutput(format string, data []byte, output io.Writer) error {
	if format == formatYAML {
		_, err := output.Write([]byte("---\n"))
		if err != nil {
			return fmt.Errorf("failed to write YAML separator: %v", err)
		}
	}
	_, err := output.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write data: %v", err)
	}

	return nil
}

func (m command) generateSpec() (*specs.Spec, error) {
	nvmllib := nvml.New()
	if r := nvmllib.Init(); r != nvml.SUCCESS {
		return nil, r
	}
	defer nvmllib.Shutdown()

	devicelib := device.New(device.WithNvml(nvmllib))

	deviceSpecs, err := m.generateDeviceSpecs(devicelib)
	if err != nil {
		return nil, fmt.Errorf("failed to create device CDI specs: %v", err)
	}

	allDevice := createAllDevice(deviceSpecs)

	deviceSpecs = append(deviceSpecs, allDevice)

	allEdits := cdi.ContainerEdits{}

	ipcs, err := NewIPCDiscoverer(m.logger, "")
	if err != nil {
		return nil, fmt.Errorf("failed to create discoverer for IPC sockets: %v", err)
	}

	ipcEdits, err := edits.FromDiscoverer(ipcs)
	if err != nil {
		return nil, fmt.Errorf("failed to create container edits for IPC sockets: %v", err)
	}
	// TODO: We should not have to update this after the fact
	for _, s := range ipcEdits.Mounts {
		s.Options = append(s.Options, "noexec")
	}

	allEdits.Append(ipcEdits)

	common, err := NewCommonDiscoverer(m.logger, "", nvmllib)
	if err != nil {
		return nil, fmt.Errorf("failed to create discoverer for common entities: %v", err)
	}

	deviceFolderPermissionHooks, err := NewDeviceFolderPermissionHookDiscoverer(m.logger, "", deviceSpecs)
	if err != nil {
		return nil, fmt.Errorf("failed to generated permission hooks for device nodes: %v", err)
	}

	commonEdits, err := edits.FromDiscoverer(discover.Merge(common, deviceFolderPermissionHooks))
	if err != nil {
		return nil, fmt.Errorf("failed to create container edits for common entities: %v", err)
	}

	allEdits.Append(commonEdits)

	// Construct the spec
	// TODO: Use the code to determine the minimal version
	spec := specs.Spec{
		Version:        "0.4.0",
		Kind:           "nvidia.com/gpu",
		Devices:        deviceSpecs,
		ContainerEdits: *allEdits.ContainerEdits,
	}

	return &spec, nil
}

func (m command) generateDeviceSpecs(devicelib device.Interface) ([]specs.Device, error) {
	var deviceSpecs []specs.Device

	err := devicelib.VisitDevices(func(i int, d device.Device) error {
		isMigEnabled, err := d.IsMigEnabled()
		if err != nil {
			return fmt.Errorf("failed to check whether device is MIG device: %v", err)
		}
		if isMigEnabled {
			return nil
		}
		device, err := NewFullGPUDiscoverer(m.logger, "", d)
		if err != nil {
			return fmt.Errorf("failed to create device: %v", err)
		}

		deviceEdits, err := edits.FromDiscoverer(device)
		if err != nil {
			return fmt.Errorf("failed to create container edits for device: %v", err)
		}

		deviceSpec := specs.Device{
			Name:           fmt.Sprintf("gpu%d", i),
			ContainerEdits: *deviceEdits.ContainerEdits,
		}

		deviceSpecs = append(deviceSpecs, deviceSpec)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to generate CDI spec for GPU devices: %v", err)
	}

	err = devicelib.VisitMigDevices(func(i int, d device.Device, j int, mig device.MigDevice) error {
		device, err := NewMigDeviceDiscoverer(m.logger, "", d, mig)
		if err != nil {
			return fmt.Errorf("failed to create MIG device: %v", err)
		}

		deviceEdits, err := edits.FromDiscoverer(device)
		if err != nil {
			return fmt.Errorf("failed to create container edits for MIG device: %v", err)
		}

		deviceSpec := specs.Device{
			Name:           fmt.Sprintf("mig%v:%v", i, j),
			ContainerEdits: *deviceEdits.ContainerEdits,
		}

		deviceSpecs = append(deviceSpecs, deviceSpec)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("falied to generate CDI spec for MIG devices: %v", err)
	}

	return deviceSpecs, nil
}

// createAllDevice creates an 'all' device which combines the edits from the previous devices
func createAllDevice(deviceSpecs []specs.Device) specs.Device {
	edits := cdi.ContainerEdits{}

	for _, d := range deviceSpecs {
		edit := cdi.ContainerEdits{
			ContainerEdits: &d.ContainerEdits,
		}
		edits.Append(&edit)
	}

	all := specs.Device{
		Name:           "all",
		ContainerEdits: *edits.ContainerEdits,
	}
	return all
}

// createParentDirsIfRequired creates the parent folders of the specified path if requried.
// Note that MkdirAll does not specifically check whether the specified path is non-empty and raises an error if it is.
// The path will be empty if filename in the current folder is specified, for example
func createParentDirsIfRequired(filename string) error {
	dir := filepath.Dir(filename)
	if dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0755)
}
