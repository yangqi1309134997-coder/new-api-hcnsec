/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { createSectionRegistry } from '../utils/section-registry'
import { ChannelDispatchSection } from './channel-dispatch-section'
import { ChannelHealthSection } from './channel-health-section'
import type { RequestPolicySettings } from './defaults'
import { RequestChecksSection } from './request-checks-section'
import { ViolationGuardSection } from './violation-guard-section'
import { RealNameSection } from './realname-section'
import { RoutingPolicySection } from './routing-section'

const POLICY_SECTIONS = [
  {
    id: 'filtering',
    titleKey: 'Request checks',
    build: (settings: RequestPolicySettings) => (
      <RequestChecksSection
        defaultValues={{
          CheckSensitiveEnabled: settings.CheckSensitiveEnabled,
          CheckSensitiveOnPromptEnabled: settings.CheckSensitiveOnPromptEnabled,
          SensitiveWords: settings.SensitiveWords,
        }}
      />
    ),
  },
  {
    id: 'guard',
    titleKey: 'Violation Guard',
    build: (settings: RequestPolicySettings) => (
      // 整包下发：此处若逐字段挑选，新增配置键极易被遗漏，
      // 导致表单取不到值（undefined）而校验报错、保存被拦
      <ViolationGuardSection defaultValues={settings} />
    ),
  },
  {
    id: 'realname',
    titleKey: 'Real-name authentication',
    build: (settings: RequestPolicySettings) => (
      <RealNameSection
        defaultValues={{
          RealNameEnabled: settings.RealNameEnabled,
          RealNameMethod: settings.RealNameMethod,
          RealNameApplyGroups: settings.RealNameApplyGroups,
          RealNameUserWhitelist: settings.RealNameUserWhitelist,
          RealNameBlockRelay: settings.RealNameBlockRelay,
          RealNameBlockTopup: settings.RealNameBlockTopup,
          RealNameBlockMessage: settings.RealNameBlockMessage,
          RealNameNoticeText: settings.RealNameNoticeText,
          RealNameTieredId2Trigger: settings.RealNameTieredId2Trigger,
          RealNameTieredQuotaThreshold: settings.RealNameTieredQuotaThreshold,
          RealNameOneIdOneAccount: settings.RealNameOneIdOneAccount,
          RealNameMinAge: settings.RealNameMinAge,
          RealNameId2Provider: settings.RealNameId2Provider,
          RealNameId2AppCode: settings.RealNameId2AppCode,
          RealNameId2SecretKey: settings.RealNameId2SecretKey,
          RealNameId2Endpoint: settings.RealNameId2Endpoint,
          RealNameId2BodyTemplate: settings.RealNameId2BodyTemplate,
          RealNameId2SuccessRule: settings.RealNameId2SuccessRule,
          RealNameSmsProvider: settings.RealNameSmsProvider,
          RealNameSmsAccessKey: settings.RealNameSmsAccessKey,
          RealNameSmsSecretKey: settings.RealNameSmsSecretKey,
          RealNameSmsSignName: settings.RealNameSmsSignName,
          RealNameSmsTemplateCode: settings.RealNameSmsTemplateCode,
          RealNameSmsAppId: settings.RealNameSmsAppId,
          RealNameSmsEndpoint: settings.RealNameSmsEndpoint,
          RealNameSmsBodyTemplate: settings.RealNameSmsBodyTemplate,
          RealNameSmsAuthHeader: settings.RealNameSmsAuthHeader,
          RealNameSmsCodeTTL: settings.RealNameSmsCodeTTL,
          RealNamePhonePattern: settings.RealNamePhonePattern,
          RealNameProviderTimeoutSec: settings.RealNameProviderTimeoutSec,
        }}
      />
    ),
  },
  {
    id: 'routing',
    titleKey: 'Sessions and retries',
    build: () => <RoutingPolicySection />,
  },
  {
    id: 'dispatch',
    titleKey: 'Channel dispatch',
    build: (settings: RequestPolicySettings) => (
      <ChannelDispatchSection defaultValues={settings} />
    ),
  },
  {
    id: 'health',
    titleKey: 'Channel health',
    build: (settings: RequestPolicySettings) => (
      <ChannelHealthSection defaultValues={settings} />
    ),
  },
] as const

export type PolicySectionId = (typeof POLICY_SECTIONS)[number]['id']
const registry = createSectionRegistry<PolicySectionId, RequestPolicySettings>({
  sections: POLICY_SECTIONS,
  defaultSection: 'routing',
  basePath: '/system-settings/request-policies',
  urlStyle: 'path',
})
export const POLICY_SECTION_IDS = registry.sectionIds
export const getPolicySectionNavItems = registry.getSectionNavItems
export const getPolicySectionContent = registry.getSectionContent
export const getPolicySectionMeta = registry.getSectionMeta
