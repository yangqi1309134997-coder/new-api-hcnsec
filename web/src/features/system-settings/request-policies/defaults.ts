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
import type { ChannelAffinitySettings } from '../general/channel-affinity/types'
import type { SecuritySettings } from '../types'

export type RetrySettings = {
  RetryTimes: number
  AutomaticRetryStatusCodes: string
}
export type HealthSettings = {
  ChannelDisableThreshold: string
  AutomaticDisableChannelEnabled: boolean
  AutomaticEnableChannelEnabled: boolean
  AutomaticDisableKeywords: string
  AutomaticDisableStatusCodes: string
  'monitor_setting.auto_test_channel_enabled': boolean
  'monitor_setting.auto_test_channel_minutes': number
  'monitor_setting.channel_test_concurrency': number
  'monitor_setting.channel_test_mode':
    | 'scheduled_all'
    | 'auto_ban_only'
    | 'passive_recovery'
}
export type ChannelDispatchSettings = {
  ChannelLoadBalanceStrategy: string
  'loadbalance_setting.by_group': string
  'loadbalance_setting.smart_top_n': number
  'loadbalance_setting.smart_explore_percent': number
  'loadbalance_setting.smart_fail_cooldown_seconds': number
  'loadbalance_setting.smart_min_samples': number
  'loadbalance_setting.smart_load_penalty': number
  'loadbalance_setting.smart_latency_half_life_minutes': number
  'auto_model_setting.enabled': boolean
  'auto_model_setting.pool': string[]
}
export type FilteringSettings = Pick<
  SecuritySettings,
  'CheckSensitiveEnabled' | 'CheckSensitiveOnPromptEnabled' | 'SensitiveWords'
>
export type VGuardSettings = {
  VGuardEnabled: boolean
  VGuardStrongWords: string
  VGuardWeakWords: string
  VGuardWeakThreshold: number
  VGuardWindowMinutes: number
  VGuardBlockHours: number
  VGuardInjectEnabled: boolean
  VGuardInjectPrompt: string
  VGuardApplyGroups: string
  VGuardUserWhitelist: string
  VGuardBanTiers: string
  VGuardOutputCheckEnabled: boolean
  VGuardWarnEmailEnabled: boolean
  VGuardBanEmailEnabled: boolean
  VGuardWarnEmailSubject: string
  VGuardWarnEmailBody: string
  VGuardBanEmailSubject: string
  VGuardBanEmailBody: string
  VGuardEmailThrottlePerDay: number
  VGuardLLMEnabled: boolean
  VGuardLLMProtocol: string
  VGuardLLMMode: string
  VGuardLLMBaseURL: string
  VGuardLLMKey: string
  VGuardLLMModel: string
  VGuardLLMPromptTemplate: string
  VGuardLLMSoInstructions: string
  VGuardLLMSoUnsafeCriteria: string
  VGuardLLMSoSafeCriteria: string
  VGuardLLMSoNoulInstruction: string
  VGuardLLMTrigger: string
  VGuardLLMAction: string
  VGuardLLMSustainHours: number
  VGuardLLMTimeoutSec: number
  VGuardLLMMaxTextChars: number
  VGuardLLMUnsafePattern: string
  VGuardLLMSafePattern: string
  VGuardLLMFailOpen: boolean
  VGuardLLMAsyncEnabled: boolean
  VGuardLLMFastSeconds: number
  VGuardLLMAsyncSeconds: number
  VGuardLLMMaxConcurrent: number
  VGuardLLMErrorCooldownSec: number
  VGuardLLMModels: string
  VGuardLLMSafeExitStreak: number
  VGuardLLMSafeExitFloorMin: number
  VGuardLLMWeakReenter: boolean
}
export type RequestPolicySettings = RetrySettings &
  HealthSettings &
  ChannelDispatchSettings &
  FilteringSettings &
  VGuardSettings &
  RealNameSettings &
  Pick<ChannelAffinitySettings, keyof ChannelAffinitySettings>

export const defaultRealNameSettings: RealNameSettings = {
  RealNameEnabled: false,
  RealNameMethod: 'phone',
  RealNameApplyGroups: '',
  RealNameUserWhitelist: '',
  RealNameBlockRelay: false,
  RealNameBlockTopup: false,
  RealNameBlockMessage: '',
  RealNameNoticeText: '',
  RealNameTieredId2Trigger: 'topup',
  RealNameTieredQuotaThreshold: 0,
  RealNameOneIdOneAccount: true,
  RealNameMinAge: 0,
  RealNameId2Provider: 'aliyun-market',
  RealNameId2AppCode: '',
  RealNameId2SecretKey: '',
  RealNameId2Endpoint: '',
  RealNameId2BodyTemplate: '',
  RealNameId2SuccessRule: '',
  RealNameSmsProvider: 'aliyun',
  RealNameSmsAccessKey: '',
  RealNameSmsSecretKey: '',
  RealNameSmsSignName: '',
  RealNameSmsTemplateCode: '',
  RealNameSmsAppId: '',
  RealNameSmsEndpoint: '',
  RealNameSmsBodyTemplate: '',
  RealNameSmsAuthHeader: '',
  RealNameSmsCodeTTL: 300,
  RealNamePhonePattern: '^1[3-9]\\d{9}$',
  RealNameProviderTimeoutSec: 10,
}

export const defaultRequestPolicySettings: RequestPolicySettings = {
  ...defaultRealNameSettings,
  RetryTimes: 0,
  AutomaticRetryStatusCodes:
    '100-199,300-399,401-407,409-499,500-503,505-523,525-599',
  ChannelDisableThreshold: '',
  AutomaticDisableChannelEnabled: false,
  AutomaticEnableChannelEnabled: false,
  AutomaticDisableKeywords: '',
  AutomaticDisableStatusCodes: '401',
  'monitor_setting.auto_test_channel_enabled': false,
  'monitor_setting.auto_test_channel_minutes': 10,
  'monitor_setting.channel_test_concurrency': 1,
  'monitor_setting.channel_test_mode': 'scheduled_all',
  ChannelLoadBalanceStrategy: 'weighted-random',
  'loadbalance_setting.by_group': '{"default":"smart"}',
  'loadbalance_setting.smart_top_n': 12,
  'loadbalance_setting.smart_explore_percent': 5,
  'loadbalance_setting.smart_fail_cooldown_seconds': 45,
  'loadbalance_setting.smart_min_samples': 10,
  'loadbalance_setting.smart_load_penalty': 1,
  'loadbalance_setting.smart_latency_half_life_minutes': 10,
  'auto_model_setting.enabled': true,
  'auto_model_setting.pool': [
    'DeepSeek-V4-Flash',
    'DeepSeek-V4-Pro',
    'DeepSeek-V4.1-Flash',
    'glm-5.3',
    'glm-5.3-flash',
    'MiMo-V2.6-Flash',
    'MiniCPM-V-4.7-35B-A3B',
    'MiniMax-M3.1-Flash',
    'Qwen3.8-27B',
    'Qwen3.8-Flash-Next',
    'sensenova-6.8-flash-lite',
    'sensenova-u1.5-lite',
    'step-5-preview',
  ],
  'channel_affinity_setting.enabled': false,
  'channel_affinity_setting.session_mode': '',
  'channel_affinity_setting.switch_on_success': true,
  'channel_affinity_setting.keep_on_channel_disabled': false,
  'channel_affinity_setting.max_entries': 100000,
  'channel_affinity_setting.default_ttl_seconds': 3600,
  'channel_affinity_setting.rules': '[]',
  CheckSensitiveEnabled: false,
  CheckSensitiveOnPromptEnabled: false,
  SensitiveWords: '',
  VGuardEnabled: false,
  VGuardStrongWords: '',
  VGuardWeakWords: '',
  VGuardWeakThreshold: 10,
  VGuardWindowMinutes: 120,
  VGuardBlockHours: 4,
  VGuardInjectEnabled: true,
  VGuardInjectPrompt: '',
  VGuardApplyGroups: '',
  VGuardUserWhitelist: '',
  VGuardBanTiers: '4,24,168,720',
  VGuardOutputCheckEnabled: true,
  VGuardWarnEmailEnabled: false,
  VGuardBanEmailEnabled: false,
  VGuardWarnEmailSubject: '',
  VGuardWarnEmailBody: '',
  VGuardBanEmailSubject: '',
  VGuardBanEmailBody: '',
  VGuardEmailThrottlePerDay: 3,
  VGuardLLMEnabled: false,
  VGuardLLMProtocol: 'chat',
  VGuardLLMMode: 'safety',
  VGuardLLMBaseURL: '',
  VGuardLLMKey: '',
  VGuardLLMModel: '',
  VGuardLLMPromptTemplate: '',
  VGuardLLMSoInstructions:
    '这条用户消息是否违反内容安全规范（色情、涉未成年不当内容、暴恐、制毒制爆、诈骗黑产等）？',
  VGuardLLMSoUnsafeCriteria: '消息包含色情露骨内容、制毒制爆、诈骗黑产、暴恐等违规内容。',
  VGuardLLMSoSafeCriteria: '消息是正常无害的日常或教育类内容。',
  VGuardLLMSoNoulInstruction:
    '这条用户消息是否违反内容安全规范（色情、暴恐、制毒、诈骗等）？',
  VGuardLLMTrigger: 'weak_word',
  VGuardLLMAction: 'block',
  VGuardLLMSustainHours: 6,
  VGuardLLMTimeoutSec: 30,
  VGuardLLMMaxTextChars: 60000,
  VGuardLLMUnsafePattern: '(?i)\\bunsafe\\b',
  VGuardLLMSafePattern: '(?i)\\bsafe\\b',
  VGuardLLMFailOpen: true,
  VGuardLLMAsyncEnabled: true,
  VGuardLLMFastSeconds: 10,
  VGuardLLMAsyncSeconds: 300,
  VGuardLLMMaxConcurrent: 3,
  VGuardLLMErrorCooldownSec: 300,
  VGuardLLMModels:
    'nvidia/nemotron-3.5-content-safety,chat,safety\nd3-flash,systemone\nClef,systemone\nd3,systemone',
  VGuardLLMSafeExitStreak: 3,
  VGuardLLMSafeExitFloorMin: 5,
  VGuardLLMWeakReenter: true,
}

export type RealNameSettings = {
  RealNameEnabled: boolean
  RealNameMethod: string
  RealNameApplyGroups: string
  RealNameUserWhitelist: string
  RealNameBlockRelay: boolean
  RealNameBlockTopup: boolean
  RealNameBlockMessage: string
  RealNameNoticeText: string
  RealNameTieredId2Trigger: string
  RealNameTieredQuotaThreshold: number
  RealNameOneIdOneAccount: boolean
  RealNameMinAge: number
  RealNameId2Provider: string
  RealNameId2AppCode: string
  RealNameId2SecretKey: string
  RealNameId2Endpoint: string
  RealNameId2BodyTemplate: string
  RealNameId2SuccessRule: string
  RealNameSmsProvider: string
  RealNameSmsAccessKey: string
  RealNameSmsSecretKey: string
  RealNameSmsSignName: string
  RealNameSmsTemplateCode: string
  RealNameSmsAppId: string
  RealNameSmsEndpoint: string
  RealNameSmsBodyTemplate: string
  RealNameSmsAuthHeader: string
  RealNameSmsCodeTTL: number
  RealNamePhonePattern: string
  RealNameProviderTimeoutSec: number
}

