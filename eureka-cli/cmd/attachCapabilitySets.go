/*
Copyright © 2025 Open Library Foundation

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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/folio-org/eureka-cli/internal"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	attachCapabilitySetsCommand string = "Attach Capability Sets"

	NewLinePattern      string = `\r?\n`
	KafkaUrl            string = "kafka.eureka:9092"
	ConsumerGroupSuffix string = "mod-roles-keycloak-capability-group"

	NoActiveMembersErrorMessage string = "Consumer group 'folio-mod-roles-keycloak-capability-group' has no active members."
	IsRebalancingErrorMessage   string = "Consumer group 'folio-mod-roles-keycloak-capability-group' is rebalancing."
)

// attachCapabilitySetsCmd represents the attachCapabilitySets command
var attachCapabilitySetsCmd = &cobra.Command{
	Use:   "attachCapabilitySets",
	Short: "Attach capability sets",
	Long:  `Attach capability sets to roles.`,
	Run: func(cmd *cobra.Command, args []string) {
		AttachCapabilitySets(time.Duration(3 * time.Second))
	},
}

func AttachCapabilitySets(initialWaitDuration time.Duration) {
	vaultRootToken := GetVaultRootToken()

	for _, tenantValue := range internal.GetTenants(attachCapabilitySetsCommand, withEnableDebug, false) {
		tenantMapEntry := tenantValue.(map[string]any)

		existingTenant := tenantMapEntry["name"].(string)
		if !internal.HasTenant(existingTenant) {
			continue
		}

		slog.Info(attachCapabilitySetsCommand, internal.GetFuncName(), "### POLLING FOR CAPABILITY SETS CREATION ###")
		time.Sleep(initialWaitDuration)
		pollCapabilitySetsCreation(withEnableDebug, existingTenant)

		slog.Info(attachCapabilitySetsCommand, internal.GetFuncName(), fmt.Sprintf("### ATTACHING CAPABILITY SETS TO ROLES FOR %s TENANT ###", existingTenant))
		keycloakAccessToken := internal.GetKeycloakAccessToken(attachCapabilitySetsCommand, withEnableDebug, vaultRootToken, existingTenant)
		internal.AttachCapabilitySetsToRoles(attachCapabilitySetsCommand, withEnableDebug, existingTenant, keycloakAccessToken)
	}
}

func pollCapabilitySetsCreation(enableDebug bool, tenant string) {
	consumerGroup := fmt.Sprintf("%s-%s", viper.GetString(internal.EnvironmentFolioKey), ConsumerGroupSuffix)

	var lag int
	for {
		lag := getConsumerGroupLag(enableDebug, tenant, consumerGroup, lag)
		if lag == 0 {
			break
		}

		slog.Info(attachCapabilitySetsCommand, internal.GetFuncName(), fmt.Sprintf("Waiting for %s consumer group to process all messages, lag: %d", consumerGroup, lag))
		time.Sleep(30 * time.Second)
	}

	slog.Info(attachCapabilitySetsCommand, internal.GetFuncName(), fmt.Sprintf("Consumer group %s has no new message to process", consumerGroup))
}

func getConsumerGroupLag(enableDebug bool, tenant string, consumerGroup string, initialLag int) (lag int) {
	stdout, stderr := internal.RunCommandReturnOutput(listSystemCommand, exec.Command("docker", "exec", "-i", "kafka", "bash", "-c",
		fmt.Sprintf("kafka-consumer-groups.sh --bootstrap-server %s --describe --group %s | grep %s | awk '{print $6}'", KafkaUrl, consumerGroup, tenant)))
	if stderr.Len() > 0 {
		if strings.Contains(stderr.String(), NoActiveMembersErrorMessage) || strings.Contains(stderr.String(), IsRebalancingErrorMessage) {
			internal.LogWarn(attachCapabilitySetsCommand, enableDebug, fmt.Sprintf("internal.RunCommandReturnOutput warning - %s", stderr.String()))
			return initialLag
		}
		internal.LogErrorPrintStderrPanic(attachCapabilitySetsCommand, "internal.RunCommandReturnOutput error", stderr.String())

		return 0
	}

	lag, err := strconv.Atoi(strings.TrimSpace(regexp.MustCompile(NewLinePattern).ReplaceAllString(stdout.String(), "")))
	if err != nil {
		slog.Error(attachCapabilitySetsCommand, internal.GetFuncName(), fmt.Sprintf("strconv.Atoi warning - %s", stderr.String()))
		return initialLag
	}

	return lag
}

func init() {
	rootCmd.AddCommand(attachCapabilitySetsCmd)
}
