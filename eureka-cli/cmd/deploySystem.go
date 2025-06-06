/*
Copyright © 2024 EPAM_Systems/Thunderjet/Boburbek_Kadirkhodjaev

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package cmd

import (
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	"github.com/folio-org/eureka-cli/internal"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/spf13/cobra"
)

const (
	deploySystemCommand string = "Deploy System"

	folioKeycloakDir string = "folio-keycloak"
	folioKongDir     string = "folio-kong"

	defaultFolioKeycloakBranchName plumbing.ReferenceName = "master"
	defaultFolioKongBranchName     plumbing.ReferenceName = "master"
)

var coreRequiredContainers = []string{"postgres", "zookeeper", "kafka", "vault", "keycloak", "keycloak-internal", "kong"}

// deploySystemCmd represents the deploySystem command
var deploySystemCmd = &cobra.Command{
	Use:   "deploySystem",
	Short: "Undeploy system",
	Long:  `Undeploy all system containers.`,
	Run: func(cmd *cobra.Command, args []string) {
		DeploySystem()
	},
}

func DeploySystem() {
	homeMiscDir := internal.GetHomeMiscDir(deploySystemCommand)
	cloneUpdateSystemComponents(homeMiscDir)

	var preparedCommands []*exec.Cmd
	if withBuildImages {
		preparedCommands = []*exec.Cmd{exec.Command("docker", "compose", "--progress", "plain", "--ansi", "never", "--project-name", "eureka", "build", "--no-cache")}
	}

	subCommand := []string{"compose", "--progress", "plain", "--ansi", "never", "--project-name", "eureka", "up", "--detach"}
	if withOnlyRequired {
		requiredContainers := internal.GetRequiredContainers(deploySystemCommand, coreRequiredContainers)
		subCommand = append(subCommand, requiredContainers...)
	}

	slog.Info(deploySystemCommand, internal.GetFuncName(), "### DEPLOYING SYSTEM CONTAINERS ###")
	preparedCommands = append(preparedCommands, exec.Command("docker", subCommand...))
	for _, preparedCommand := range preparedCommands {
		internal.RunCommandFromDir(deploySystemCommand, preparedCommand, homeMiscDir)
	}

	slog.Info(deploySystemCommand, internal.GetFuncName(), "### WAITING FOR SYSTEM TO INITIALIZE ###")
	time.Sleep(15 * time.Second)
	slog.Info(deploySystemCommand, internal.GetFuncName(), "All system containers have initialized")
}

func cloneUpdateSystemComponents(homeMiscDir string) {
	slog.Info(deploySystemCommand, internal.GetFuncName(), "### CLONING & UPDATING SYSTEM COMPONENTS ###")

	folioKeycloakOutputDir := fmt.Sprintf("%s/%s", homeMiscDir, folioKeycloakDir)
	folioKongOutputDir := fmt.Sprintf("%s/%s", homeMiscDir, folioKongDir)

	slog.Info(deploySystemCommand, internal.GetFuncName(), fmt.Sprintf("Cloning %s from a %s branch", folioKeycloakDir, defaultFolioKeycloakBranchName))
	internal.GitCloneRepository(deploySystemCommand, withEnableDebug, internal.FolioKeycloakRepositoryUrl, defaultFolioKeycloakBranchName, folioKeycloakOutputDir, false)

	slog.Info(deploySystemCommand, internal.GetFuncName(), fmt.Sprintf("Cloning %s from a %s branch", folioKongDir, defaultFolioKongBranchName))
	internal.GitCloneRepository(deploySystemCommand, withEnableDebug, internal.FolioKongRepositoryUrl, defaultFolioKongBranchName, folioKongOutputDir, false)

	if withUpdateCloned {
		slog.Info(deploySystemCommand, internal.GetFuncName(), fmt.Sprintf("Pulling updates for %s from origin", folioKeycloakDir))
		internal.GitResetHardPullFromOriginRepository(deploySystemCommand, withEnableDebug, internal.FolioKeycloakRepositoryUrl, defaultFolioKeycloakBranchName, folioKeycloakOutputDir)

		slog.Info(deploySystemCommand, internal.GetFuncName(), fmt.Sprintf("Pulling updates for %s from origin", folioKongDir))
		internal.GitResetHardPullFromOriginRepository(deploySystemCommand, withEnableDebug, internal.FolioKongRepositoryUrl, defaultFolioKongBranchName, folioKongOutputDir)
	}
}

func init() {
	rootCmd.AddCommand(deploySystemCmd)
	deploySystemCmd.PersistentFlags().BoolVarP(&withBuildImages, "buildImages", "b", false, "Build Docker images")
	deploySystemCmd.PersistentFlags().BoolVarP(&withUpdateCloned, "updateCloned", "u", false, "Update Git cloned projects")
	deploySystemCmd.PersistentFlags().BoolVarP(&withOnlyRequired, "onlyRequired", "R", false, "Use only required system containers")
}
