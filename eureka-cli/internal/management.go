package internal

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

const (
	GatewayPort int = 8000

	ApplicationsPort       int = 9901
	TenantsPort            int = 9902
	TenantEntitlementsPort int = 9903

	JsonContentType           string = "application/json"
	FormUrlEncodedContentType string = "application/x-www-form-urlencoded"

	ContentTypeHeader   string = "Content-Type"
	AuthorizationHeader string = "Authorization"
	TenantHeader        string = "X-Okapi-Tenant"
	TokenHeader         string = "X-Okapi-Token"

	RemoveRoleUnsupported bool = true

	HealtcheckUri         string = "/admin/health"
	HealtcheckMaxAttempts int    = 50

	MgrTenantsUrl            string = "http://mgr-tenants.eureka:8081"
	MgrApplicationsUrl       string = "http://mgr-applications.eureka:8081"
	MgrTenantEntitlementsUrl string = "http://mgr-tenant-entitlements.eureka:8081"

	ModuleIdPattern string = "([a-z-_]+)([\\d-_.]+)([a-zA-Z0-9-_.]+)"
)

var (
	HealthcheckDefaultDuration time.Duration = 30 * time.Second

	ModuleIdRegexp *regexp.Regexp = regexp.MustCompile(ModuleIdPattern)
)

// ######## Auxiliary ########

func ExtractModuleNameAndVersion(commandName string, enableDebug bool, registryModulesMap map[string][]*RegistryModule) {
	for registryName, registryModules := range registryModulesMap {
		slog.Info(commandName, GetFuncName(), fmt.Sprintf("Extracting %s registry module names and versions", registryName))

		for moduleIndex, module := range registryModules {
			if module.Id == "okapi" {
				continue
			}

			module.Name = TrimModuleName(ModuleIdRegexp.ReplaceAllString(module.Id, `$1`))
			moduleVersion := ModuleIdRegexp.ReplaceAllString(module.Id, `$2$3`)
			module.Version = &moduleVersion
			module.SidecarName = fmt.Sprintf("%s-sc", module.Name)

			registryModules[moduleIndex] = module
		}
	}
}

func PerformModuleHealthcheck(commandName string, enableDebug bool, waitMutex *sync.WaitGroup, moduleName string, port int) {
	slog.Info(commandName, GetFuncName(), fmt.Sprintf("Waiting for module container %s on port %d to initialize", moduleName, port))
	requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), port, HealtcheckUri)
	healthcheckAttempts := HealtcheckMaxAttempts
	for {
		time.Sleep(HealthcheckDefaultDuration)

		isHealthyVertxContainer := false
		actuatorHealthStr := DoGetDecodeReturnString(commandName, requestUrl, enableDebug, false, map[string]string{})
		if strings.Contains(actuatorHealthStr, "OK") {
			isHealthyVertxContainer = !isHealthyVertxContainer
		}

		isHealthySpringBootContainer := false
		actuatorHealthMap := DoGetDecodeReturnMapStringAny(commandName, requestUrl, enableDebug, false, map[string]string{})
		if actuatorHealthMap != nil && strings.Contains(actuatorHealthMap["status"].(string), "UP") {
			isHealthySpringBootContainer = !isHealthySpringBootContainer
		}

		if isHealthyVertxContainer || isHealthySpringBootContainer {
			slog.Info(commandName, GetFuncName(), fmt.Sprintf("Module container %s is healthy", moduleName))
			waitMutex.Done()
			break
		}

		healthcheckAttempts--
		if healthcheckAttempts > 0 {
			slog.Info(commandName, GetFuncName(), fmt.Sprintf("Module container %s is unhealthy, %d/%d attempts left", moduleName, healthcheckAttempts, HealtcheckMaxAttempts))
		} else {
			slog.Info(commandName, GetFuncName(), fmt.Sprintf("Module container %s is unhealthy, out of attempts", moduleName))
			waitMutex.Done()
			LogErrorPanic(commandName, fmt.Sprintf("internal.PerformModuleHealthcheck - Module container %s did not initialize, cannot continue", moduleName))
		}
	}
}

// ######## Application & Application Discovery ########

func GetApplications(commandName string, enableDebug bool, panicOnError bool) Applications {
	var applications Applications

	requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), ApplicationsPort, "/applications")

	resp := DoGetReturnResponse(commandName, requestUrl, enableDebug, panicOnError, map[string]string{})
	if resp == nil {
		return applications
	}
	defer resp.Body.Close()

	err := json.NewDecoder(resp.Body).Decode(&applications)
	if err != nil {
		slog.Error(commandName, GetFuncName(), "json.NewDecoder error")
		panic(err)
	}

	return applications
}

func RemoveApplications(commandName string, enableDebug bool, panicOnError bool) {
	applications := GetApplications(commandName, enableDebug, panicOnError)

	for _, value := range applications.ApplicationDescriptors {
		id := value["id"].(string)

		requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), ApplicationsPort, fmt.Sprintf("/applications/%s", id))

		DoDelete(commandName, requestUrl, enableDebug, false, map[string]string{})

		slog.Info(commandName, GetFuncName(), fmt.Sprintf(`Removed %s application`, id))
	}
}

func CreateApplications(commandName string, enableDebug bool, dto *RegisterModuleDto) {
	var (
		backendModules            []map[string]string
		frontendModules           []map[string]string
		backendModuleDescriptors  []any
		frontendModuleDescriptors []any
		dependencies              map[string]any
		discoveryModules          []map[string]string
	)

	applicationMap := viper.GetStringMap(ApplicationKey)
	applicationName := applicationMap["name"].(string)
	applicationVersion := applicationMap["version"].(string)
	applicationPlatform := applicationMap["platform"].(string)
	applicationFetchDescriptors := applicationMap["fetch-descriptors"].(bool)

	if applicationMap["dependencies"] != nil {
		dependencies = applicationMap["dependencies"].(map[string]any)
	}

	for registryName, registryModules := range dto.RegistryModules {
		slog.Info(commandName, GetFuncName(), fmt.Sprintf("Registering %s modules", registryName))

		for _, module := range registryModules {
			if strings.Contains(module.Name, ManagementModulePattern) {
				continue
			}

			backendModule, okBackend := dto.BackendModulesMap[module.Name]
			frontendModule, okFrontend := dto.FrontendModulesMap[module.Name]
			if (!okBackend && !okFrontend) || (okBackend && !backendModule.DeployModule || okFrontend && !frontendModule.DeployModule) {
				continue
			}

			if okBackend && backendModule.ModuleVersion != nil || okFrontend && frontendModule.ModuleVersion != nil {
				if backendModule.ModuleVersion != nil {
					module.Version = backendModule.ModuleVersion
				} else if frontendModule.ModuleVersion != nil {
					module.Version = frontendModule.ModuleVersion
				}
				module.Id = fmt.Sprintf("%s-%s", module.Name, *module.Version)
			}

			moduleDescriptorUrl := fmt.Sprintf("%s/_/proxy/modules/%s", dto.RegistryUrls[FolioRegistry], module.Id)

			if applicationFetchDescriptors {
				dto.ModuleDescriptorsMap[module.Id] = DoGetDecodeReturnAny(commandName, moduleDescriptorUrl, enableDebug, true, map[string]string{})
			}

			if okBackend {
				serverPort := strconv.Itoa(backendModule.ModuleServerPort)
				backendModule := map[string]string{"id": module.Id, "name": module.Name, "version": *module.Version}

				if applicationFetchDescriptors {
					backendModuleDescriptors = append(backendModuleDescriptors, dto.ModuleDescriptorsMap[module.Id])
				} else {
					backendModule["url"] = moduleDescriptorUrl
				}

				backendModules = append(backendModules, backendModule)

				sidecarUrl := fmt.Sprintf("http://%s.eureka:%s", module.SidecarName, serverPort)

				discoveryModules = append(discoveryModules, map[string]string{"id": module.Id, "name": module.Name, "version": *module.Version, "location": sidecarUrl})
			} else if okFrontend {
				frontendModule := map[string]string{"id": module.Id, "name": module.Name, "version": *module.Version}
				if applicationFetchDescriptors {
					frontendModuleDescriptors = append(frontendModuleDescriptors, dto.ModuleDescriptorsMap[module.Id])
				} else {
					frontendModule["url"] = moduleDescriptorUrl
				}

				frontendModules = append(frontendModules, frontendModule)
			}

			slog.Info(commandName, GetFuncName(), fmt.Sprintf("Found module for registration: %s with version %s", module.Name, *module.Version))
		}
	}

	applicationId := fmt.Sprintf("%s-%s", applicationName, applicationVersion)

	applicationBytes, err := json.Marshal(map[string]any{
		"id":                  applicationId,
		"name":                applicationName,
		"version":             applicationVersion,
		"description":         "Default",
		"platform":            applicationPlatform,
		"dependencies":        dependencies,
		"modules":             backendModules,
		"uiModules":           frontendModules,
		"moduleDescriptors":   backendModuleDescriptors,
		"uiModuleDescriptors": frontendModuleDescriptors,
	})
	if err != nil {
		slog.Error(commandName, GetFuncName(), "json.Marshal error")
		panic(err)
	}

	DoPostReturnNoContent(commandName, fmt.Sprintf(GetGatewayUrlTemplate(commandName), ApplicationsPort, "/applications?check=true"), enableDebug, true, applicationBytes, map[string]string{})

	slog.Info(commandName, GetFuncName(), fmt.Sprintf(`Created %s application`, applicationId))

	if len(discoveryModules) > 0 {
		applicationDiscoveryBytes, err := json.Marshal(map[string]any{"discovery": discoveryModules})
		if err != nil {
			slog.Error(commandName, GetFuncName(), "json.Marshal error")
			panic(err)
		}

		DoPostReturnNoContent(commandName, fmt.Sprintf(GetGatewayUrlTemplate(commandName), ApplicationsPort, "/modules/discovery"), enableDebug, true, applicationDiscoveryBytes, map[string]string{})
	}

	slog.Info(commandName, GetFuncName(), fmt.Sprintf(`Created %d entries of application module discovery`, len(discoveryModules)))
}

func UpdateModuleDiscovery(commandName string, enableDebug bool, id string, sidecarUrl string, restore bool, portServer string) {
	id = strings.ReplaceAll(id, ":", "-")
	name := TrimModuleName(ModuleIdRegexp.ReplaceAllString(id, `$1`))
	if sidecarUrl == "" || restore {
		sidecarUrl = fmt.Sprintf("http://%s-sc.eureka:%s", name, portServer)
	}

	applicationDiscoveryBytes, err := json.Marshal(map[string]any{
		"id":       id,
		"name":     name,
		"version":  ModuleIdRegexp.ReplaceAllString(id, `$2$3`),
		"location": sidecarUrl,
	})
	if err != nil {
		slog.Error(commandName, GetFuncName(), "json.Marshal error")
		panic(err)
	}

	requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), ApplicationsPort, fmt.Sprintf("/modules/%s/discovery", id))

	DoPutReturnNoContent(commandName, requestUrl, enableDebug, applicationDiscoveryBytes, map[string]string{})

	slog.Info(commandName, GetFuncName(), fmt.Sprintf(`Updated application module discovery for %s module with %s sidecar URL`, name, sidecarUrl))
}

// ######## Tenants ########

func GetTenants(commandName string, enableDebug bool, panicOnError bool) []any {
	var foundTenants []any

	requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), TenantsPort, "/tenants")

	foundTenantsMap := DoGetDecodeReturnMapStringAny(commandName, requestUrl, enableDebug, panicOnError, map[string]string{})
	if foundTenantsMap["tenants"] == nil || len(foundTenantsMap["tenants"].([]any)) == 0 {
		return nil
	}

	foundTenants = foundTenantsMap["tenants"].([]any)

	return foundTenants
}

func RemoveTenants(commandName string, enableDebug bool, panicOnError bool) {
	for _, value := range GetTenants(commandName, enableDebug, panicOnError) {
		mapEntry := value.(map[string]any)
		tenant := mapEntry["name"].(string)

		if !slices.Contains(ConvertMapKeysToSlice(viper.GetStringMap(TenantsKey)), tenant) {
			continue
		}

		requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), TenantsPort, fmt.Sprintf("/tenants/%s?purge=true", mapEntry["id"].(string)))

		DoDelete(commandName, requestUrl, enableDebug, false, map[string]string{})

		slog.Info(commandName, GetFuncName(), fmt.Sprintf(`Removed %s tenant (realm)`, tenant))
	}
}

func CreateTenants(commandName string, enableDebug bool) {
	requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), TenantsPort, "/tenants")
	tenants := ConvertMapKeysToSlice(viper.GetStringMap(TenantsKey))

	for _, tenant := range tenants {
		tenantBytes, err := json.Marshal(map[string]string{"name": tenant, "description": "Default"})
		if err != nil {
			slog.Error(commandName, GetFuncName(), "json.Marshal error")
			panic(err)
		}

		DoPostReturnNoContent(commandName, requestUrl, enableDebug, true, tenantBytes, map[string]string{})

		slog.Info(commandName, GetFuncName(), fmt.Sprintf(`Created %s tenant (realm)`, tenant))
	}
}

// ######## Tenant Entitlements ########

func RemoveTenantEntitlements(commandName string, enableDebug bool, panicOnError bool) {
	requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), TenantEntitlementsPort, "/entitlements?purgeOnRollback=true&ignoreErrors=false")
	applicationMap := viper.GetStringMap(ApplicationKey)
	applicationName := applicationMap["name"].(string)
	applicationVersion := applicationMap["version"].(string)

	for _, value := range GetTenants(commandName, enableDebug, panicOnError) {
		mapEntry := value.(map[string]any)
		tenant := mapEntry["name"].(string)

		if !slices.Contains(ConvertMapKeysToSlice(viper.GetStringMap(TenantsKey)), tenant) {
			continue
		}

		tenantId := mapEntry["id"].(string)

		var applications []string
		applications = append(applications, fmt.Sprintf("%s-%s", applicationName, applicationVersion))

		tenantEntitlementBytes, err := json.Marshal(map[string]any{"tenantId": tenantId, "applications": applications})
		if err != nil {
			slog.Error(commandName, GetFuncName(), "json.Marshal error")
			panic(err)
		}

		DoDeleteWithBody(commandName, requestUrl, enableDebug, tenantEntitlementBytes, true, map[string]string{})

		slog.Info(commandName, GetFuncName(), fmt.Sprintf(`Removed tenant entitlement for %s tenant (realm)`, tenant))
	}
}

func CreateTenantEntitlement(commandName string, enableDebug bool) {
	requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), TenantEntitlementsPort, "/entitlements?purgeOnRollback=true&ignoreErrors=false&tenantParameters=loadReference=true,loadSample=true")
	applicationMap := viper.GetStringMap(ApplicationKey)
	applicationName := applicationMap["name"].(string)
	applicationVersion := applicationMap["version"].(string)

	for _, value := range GetTenants(commandName, enableDebug, false) {
		mapEntry := value.(map[string]any)
		tenant := mapEntry["name"].(string)

		if !slices.Contains(ConvertMapKeysToSlice(viper.GetStringMap(TenantsKey)), tenant) {
			continue
		}

		tenantId := mapEntry["id"].(string)

		var applications []string
		applications = append(applications, fmt.Sprintf("%s-%s", applicationName, applicationVersion))

		tenantEntitlementBytes, err := json.Marshal(map[string]any{"tenantId": tenantId, "applications": applications})
		if err != nil {
			slog.Error(commandName, GetFuncName(), "json.Marshal error")
			panic(err)
		}

		DoPostReturnNoContent(commandName, requestUrl, enableDebug, true, tenantEntitlementBytes, map[string]string{})

		slog.Info(commandName, GetFuncName(), fmt.Sprintf(`Created tenant entitlement for %s tenant (realm)`, tenant))
	}
}

// ######## Users ########

func GetUsers(commandName string, enableDebug bool, panicOnError bool, tenant string, accessToken string) []any {
	var foundUsers []any

	requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), GatewayPort, "/users?offset=0&limit=10000")
	headers := map[string]string{ContentTypeHeader: JsonContentType, TenantHeader: tenant, TokenHeader: accessToken}

	foundTenantsMap := DoGetDecodeReturnMapStringAny(commandName, requestUrl, enableDebug, panicOnError, headers)
	if foundTenantsMap["users"] == nil || len(foundTenantsMap["users"].([]any)) == 0 {
		return nil
	}

	foundUsers = foundTenantsMap["users"].([]any)

	return foundUsers
}

func RemoveUsers(commandName string, enableDebug bool, panicOnError bool, tenant string, accessToken string) {
	headers := map[string]string{ContentTypeHeader: JsonContentType, TenantHeader: tenant, TokenHeader: accessToken}

	for _, value := range GetUsers(commandName, enableDebug, panicOnError, tenant, accessToken) {
		mapEntry := value.(map[string]any)
		username := mapEntry["username"].(string)
		usersMap := viper.GetStringMap(UsersKey)
		if usersMap[username] == nil {
			continue
		}

		requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), GatewayPort, fmt.Sprintf("/users-keycloak/users/%s", mapEntry["id"].(string)))

		DoDelete(commandName, requestUrl, enableDebug, false, headers)

		slog.Info(commandName, GetFuncName(), fmt.Sprintf(`Removed %s in %s tenant (realm)`, username, tenant))
	}
}

func CreateUsers(commandName string, enableDebug bool, panicOnError bool, existingTenant string, accessToken string) {
	postUserRequestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), GatewayPort, "/users-keycloak/users")
	postUserPasswordRequestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), GatewayPort, "/authn/credentials")
	postUserRoleRequestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), GatewayPort, "/roles/users")
	usersMap := viper.GetStringMap(UsersKey)

	for username, value := range usersMap {
		mapEntry := value.(map[string]any)
		tenant := mapEntry["tenant"].(string)
		if existingTenant != tenant {
			continue
		}

		password := mapEntry["password"].(string)
		firstName := mapEntry["first-name"].(string)
		lastName := mapEntry["last-name"].(string)
		userRoles := mapEntry["roles"].([]any)

		userBytes, err := json.Marshal(map[string]any{
			"username": username,
			"active":   true,
			"type":     "staff",
			"personal": map[string]any{
				"firstName":              firstName,
				"lastName":               lastName,
				"email":                  fmt.Sprintf("%s-%s", tenant, username),
				"preferredContactTypeId": "002",
			},
		})
		if err != nil {
			slog.Error(commandName, GetFuncName(), "json.Marshal error")
			panic(err)
		}

		okapiBasedHeaders := map[string]string{ContentTypeHeader: JsonContentType, TenantHeader: tenant, TokenHeader: accessToken}
		nonOkapiBasedHeaders := map[string]string{ContentTypeHeader: JsonContentType, TenantHeader: tenant, AuthorizationHeader: fmt.Sprintf("Bearer %s", accessToken)}

		createdUserMap := DoPostReturnMapStringAny(commandName, postUserRequestUrl, enableDebug, panicOnError, userBytes, okapiBasedHeaders)

		userId := createdUserMap["id"].(string)

		slog.Info(commandName, GetFuncName(), fmt.Sprintf(`Created %s user with password %s in %s tenant (realm)`, username, password, tenant))

		userPasswordBytes, err := json.Marshal(map[string]any{"userId": userId, "username": username, "password": password})
		if err != nil {
			slog.Error(commandName, GetFuncName(), "json.Marshal error")
			panic(err)
		}

		DoPostReturnNoContent(commandName, postUserPasswordRequestUrl, enableDebug, panicOnError, userPasswordBytes, nonOkapiBasedHeaders)

		slog.Info(commandName, GetFuncName(), fmt.Sprintf(`Attached %s password to %s user in %s tenant (realm)`, password, username, tenant))

		var roleIds []string
		for _, userRole := range userRoles {
			role := GetRoleByName(commandName, enableDebug, userRole.(string), okapiBasedHeaders)
			roleId := role["id"].(string)
			roleName := role["name"].(string)

			if roleId == "" {
				slog.Warn(commandName, GetFuncName(), fmt.Sprintf("internal.GetRoleByName warn - Did not find role %s by name", roleName))
				continue
			}

			roleIds = append(roleIds, roleId)
		}

		userRoleBytes, err := json.Marshal(map[string]any{"userId": userId, "roleIds": roleIds})
		if err != nil {
			slog.Error(commandName, GetFuncName(), "json.Marshal error")
			panic(err)
		}

		DoPostReturnNoContent(commandName, postUserRoleRequestUrl, enableDebug, panicOnError, userRoleBytes, okapiBasedHeaders)

		slog.Info(commandName, GetFuncName(), fmt.Sprintf(`Attached %d roles to %s user in %s tenant (realm)`, len(roleIds), username, tenant))
	}
}

// ######## Roles ########

func GetRoles(commandName string, enableDebug bool, panicOnError bool, headers map[string]string) []any {
	var foundRoles []any

	requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), GatewayPort, "/roles?offset=0&limit=10000")

	foundRolesMap := DoGetDecodeReturnMapStringAny(commandName, requestUrl, enableDebug, panicOnError, headers)
	if foundRolesMap["roles"] == nil || len(foundRolesMap["roles"].([]any)) == 0 {
		return nil
	}

	foundRoles = foundRolesMap["roles"].([]any)

	return foundRoles
}

func GetRoleByName(commandName string, enableDebug bool, roleName string, headers map[string]string) map[string]any {
	requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), GatewayPort, fmt.Sprintf("/roles?query=name==%s", roleName))

	foundRolesMap := DoGetDecodeReturnMapStringAny(commandName, requestUrl, enableDebug, true, headers)
	if foundRolesMap["roles"] == nil {
		return nil
	}

	foundRoles := foundRolesMap["roles"].([]any)
	if len(foundRoles) != 1 {
		LogErrorPanic(commandName, fmt.Sprintf("internal.GetRoleByName - Number of found roles by %s role name is not 1", roleName))
		return nil
	}

	return foundRoles[0].(map[string]any)
}

func RemoveRoles(commandName string, enableDebug bool, panicOnError bool, tenant string, accessToken string) {
	headers := map[string]string{ContentTypeHeader: JsonContentType, TenantHeader: tenant, TokenHeader: accessToken}

	for _, value := range GetRoles(commandName, enableDebug, panicOnError, headers) {
		mapEntry := value.(map[string]any)
		roleName := mapEntry["name"].(string)

		rolesMap := viper.GetStringMap(RolesKey)
		if rolesMap[roleName] == nil {
			continue
		}

		requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), GatewayPort, fmt.Sprintf("/roles-keycloak/roles/%s", mapEntry["id"].(string)))

		DoDelete(commandName, requestUrl, enableDebug, false, headers)

		slog.Info(commandName, GetFuncName(), fmt.Sprintf(`Removed %s role in %s tenant (realm)`, roleName, tenant))
	}
}

func CreateRoles(commandName string, enableDebug bool, panicOnError bool, existingTenant string, accessToken string) {
	requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), GatewayPort, "/roles")
	rolesMap := viper.GetStringMap(RolesKey)
	caser := cases.Title(language.English)

	for role, value := range rolesMap {
		mapEntry := value.(map[string]any)
		tenant := mapEntry["tenant"].(string)
		if existingTenant != tenant {
			continue
		}

		headers := map[string]string{ContentTypeHeader: JsonContentType, TenantHeader: tenant, TokenHeader: accessToken}

		roleBytes, err := json.Marshal(map[string]string{"name": caser.String(role), "description": "Default"})
		if err != nil {
			slog.Error(commandName, GetFuncName(), "json.Marshal error")
			panic(err)
		}

		DoPostReturnNoContent(commandName, requestUrl, enableDebug, panicOnError, roleBytes, headers)

		slog.Info(commandName, GetFuncName(), fmt.Sprintf(`Created %s role in %s tenant (realm)`, role, tenant))
	}
}

// ######## Capabilities ########

func GetCapabilitySets(commandName string, enableDebug bool, panicOnError bool, headers map[string]string) []any {
	var foundCapabilitySets []any

	applications := GetApplications(commandName, enableDebug, panicOnError)

	for _, value := range applications.ApplicationDescriptors {
		applicationId := value["id"].(string)

		requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), GatewayPort, fmt.Sprintf("/capability-sets?offset=0&limit=10000&query=applicationId==%s", applicationId))

		foundCapabilitySetsMap := DoGetDecodeReturnMapStringAny(commandName, requestUrl, enableDebug, panicOnError, headers)
		if foundCapabilitySetsMap["capabilitySets"] == nil || len(foundCapabilitySetsMap["capabilitySets"].([]any)) == 0 {
			return nil
		}

		foundCapabilitySets = append(foundCapabilitySets, foundCapabilitySetsMap["capabilitySets"].([]any)...)
	}

	return foundCapabilitySets
}

func GetCapabilitySetsByName(commandName string, enableDebug bool, panicOnError bool, headers map[string]string, capabilitySetName string) []any {
	var foundCapabilitySets []any

	requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), GatewayPort, fmt.Sprintf("/capability-sets?offset=0&limit=1000&query=name=%s", capabilitySetName))

	foundCapabilitySetsMap := DoGetDecodeReturnMapStringAny(commandName, requestUrl, enableDebug, panicOnError, headers)
	if foundCapabilitySetsMap["capabilitySets"] == nil || len(foundCapabilitySetsMap["capabilitySets"].([]any)) == 0 {
		return nil
	}

	foundCapabilitySets = foundCapabilitySetsMap["capabilitySets"].([]any)

	return foundCapabilitySets
}

func DetachCapabilitySetsFromRoles(commandName string, enableDebug bool, panicOnError bool, tenant string, accessToken string) {
	headers := map[string]string{ContentTypeHeader: JsonContentType, TenantHeader: tenant, TokenHeader: accessToken}

	for _, value := range GetRoles(commandName, enableDebug, panicOnError, headers) {
		mapEntry := value.(map[string]any)
		roleName := mapEntry["name"].(string)

		rolesMap := viper.GetStringMap(RolesKey)
		if rolesMap[strings.ToLower(roleName)] == nil {
			continue
		}

		requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), GatewayPort, fmt.Sprintf("/roles/%s/capability-sets", mapEntry["id"].(string)))

		DoDelete(commandName, requestUrl, enableDebug, false, headers)

		slog.Info(commandName, GetFuncName(), fmt.Sprintf(`Detached capability sets from %s role in %s tenant (realm)`, roleName, tenant))
	}
}

func AttachCapabilitySetsToRoles(commandName string, enableDebug bool, tenant string, accessToken string) {
	requestUrl := fmt.Sprintf(GetGatewayUrlTemplate(commandName), GatewayPort, "/roles/capability-sets")
	rolesMapConfig := viper.GetStringMap(RolesKey)
	headers := map[string]string{ContentTypeHeader: JsonContentType, TenantHeader: tenant, TokenHeader: accessToken}

	for _, roleValue := range GetRoles(commandName, enableDebug, true, headers) {
		roleMapEntry := roleValue.(map[string]any)
		roleId := roleMapEntry["id"].(string)
		roleName := roleMapEntry["name"].(string)
		rolesMapConfigByRole, ok := rolesMapConfig[strings.ToLower(roleName)]
		if !ok {
			continue
		}

		roleConfigMapEntry := rolesMapConfigByRole.(map[string]any)
		if tenant != roleConfigMapEntry["tenant"].(string) {
			continue
		}

		capabilitySetsConfig := roleConfigMapEntry["capability-sets"].([]any)

		var capabilitySetsMapList []map[string]any
		if len(capabilitySetsConfig) == 1 && slices.Contains(capabilitySetsConfig, "all") {
			capabilitySetsMapList = addAllCapabilitySets(commandName, enableDebug, headers, capabilitySetsMapList)
		} else {
			capabilitySetsMapList = addSelectedCapabilitySets(commandName, enableDebug, headers, capabilitySetsConfig, capabilitySetsMapList)
		}

		var capabilitySetIds []string
		for _, mapEntry := range capabilitySetsMapList {
			capabilitySetId := mapEntry["id"].(string)

			capabilitySetIds = append(capabilitySetIds, capabilitySetId)
		}

		if len(capabilitySetIds) == 0 {
			slog.Info(commandName, GetFuncName(), fmt.Sprintf(`No capability sets were attached to %s role in %s tenant (realm)`, roleName, tenant))
			continue
		}

		capabilitySetsBytes, err := json.Marshal(map[string]any{"roleId": roleId, "capabilitySetIds": capabilitySetIds})
		if err != nil {
			slog.Error(commandName, GetFuncName(), "json.Marshal error")
			panic(err)
		}

		DoPostReturnNoContent(commandName, requestUrl, enableDebug, true, capabilitySetsBytes, headers)

		slog.Info(commandName, GetFuncName(), fmt.Sprintf(`Attached %d capability sets to %s role in %s tenant (realm)`, len(capabilitySetIds), roleName, tenant))
	}
}

func addSelectedCapabilitySets(commandName string, enableDebug bool, headers map[string]string, capabilitySetsConfig []any, capabilitySetsMapList []map[string]any) []map[string]any {
	for _, capabilitySetConfig := range capabilitySetsConfig {
		capabilitySetConfigName := capabilitySetConfig.(string)

		for _, capabilityValue := range GetCapabilitySetsByName(commandName, enableDebug, true, headers, capabilitySetConfigName) {
			capabilityMapEntry := capabilityValue.(map[string]any)

			capabilitySetsMapList = append(capabilitySetsMapList, capabilityMapEntry)
		}
	}

	return capabilitySetsMapList
}

func addAllCapabilitySets(commandName string, enableDebug bool, headers map[string]string, capabilitySetsMapList []map[string]any) []map[string]any {
	for _, capabilityValue := range GetCapabilitySets(commandName, enableDebug, true, headers) {
		capabilityMapEntry := capabilityValue.(map[string]any)

		capabilitySetsMapList = append(capabilitySetsMapList, capabilityMapEntry)
	}

	return capabilitySetsMapList
}
