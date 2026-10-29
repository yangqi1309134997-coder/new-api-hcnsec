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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'
import { toast } from 'sonner'

import { api } from '@/lib/api'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { Button } from '@/components/ui/button'
import { handleServerError } from '@/lib/handle-server-error'

import {
  SettingsForm,
  SettingsControlGroup,
  SettingsControlChildren,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useSavePolicy } from './use-save-policy'

const schema = z.object({
  VGuardEnabled: z.boolean(),
  VGuardStrongWords: z.string(),
  VGuardWeakWords: z.string(),
  VGuardWeakThreshold: z.coerce.number().int().min(1),
  VGuardWindowMinutes: z.coerce.number().int().min(1),
  VGuardBlockHours: z.coerce.number().int().min(1),
  VGuardInjectEnabled: z.boolean(),
  VGuardInjectPrompt: z.string(),
  VGuardApplyGroups: z.string(),
  VGuardUserWhitelist: z.string(),
  VGuardBanTiers: z.string(),
  VGuardOutputCheckEnabled: z.boolean(),
  VGuardWarnEmailEnabled: z.boolean(),
  VGuardBanEmailEnabled: z.boolean(),
  VGuardWarnEmailSubject: z.string(),
  VGuardWarnEmailBody: z.string(),
  VGuardBanEmailSubject: z.string(),
  VGuardBanEmailBody: z.string(),
  VGuardEmailThrottlePerDay: z.coerce.number().int().min(0),
  VGuardLLMEnabled: z.boolean(),
  VGuardLLMProtocol: z.string(),
  VGuardLLMMode: z.string(),
  VGuardLLMBaseURL: z.string(),
  VGuardLLMKey: z.string(),
  VGuardLLMModel: z.string(),
  VGuardLLMPromptTemplate: z.string(),
  VGuardLLMSoInstructions: z.string(),
  VGuardLLMSoUnsafeCriteria: z.string(),
  VGuardLLMSoSafeCriteria: z.string(),
  VGuardLLMSoNoulInstruction: z.string(),
  VGuardLLMTrigger: z.string(),
  VGuardLLMAction: z.string(),
  VGuardLLMSustainHours: z.coerce.number().int().min(0),
  VGuardLLMTimeoutSec: z.coerce.number().int().min(1),
  VGuardLLMMaxTextChars: z.coerce.number().int().min(100),
  VGuardLLMUnsafePattern: z.string(),
  VGuardLLMSafePattern: z.string(),
  VGuardLLMFailOpen: z.boolean(),
  VGuardLLMAsyncEnabled: z.boolean(),
  VGuardLLMFastSeconds: z.coerce.number().int().min(1).max(300),
  VGuardLLMAsyncSeconds: z.coerce.number().int().min(3).max(600),
  VGuardLLMMaxConcurrent: z.coerce.number().int().min(0).max(64),
  VGuardLLMErrorCooldownSec: z.coerce.number().int().min(0).max(3600),
  VGuardLLMModels: z.string(),
  VGuardLLMSafeExitStreak: z.coerce.number().int().min(0).max(50),
  VGuardLLMSafeExitFloorMin: z.coerce.number().int().min(0).max(720),
  VGuardLLMWeakReenter: z.boolean(),
})

type Values = z.infer<typeof schema>

type VGuardPoolModel = {
  model: string
  protocol: string
  success_rate: number
  avg_ms: number
  cooldown_sec: number
}

type VGuardStatus = {
  enabled: boolean
  weak_threshold: number
  window_minutes: number
  block_hours: number
  strong_words_n: number
  weak_words_n: number
  apply_groups: string
  user_whitelist: string
  llm?: {
    enabled: boolean
    mode: string
    model: string
    trigger: string
    action: string
    sustain_hours: number
    pool_health?: VGuardPoolModel[]
  }
  items: Array<{
    user_id: number
    username: string
    weak_hits: number
    strong_count: number
    blocked: boolean
    blocked_until: number
  }>
}

type VGuardEventRow = {
  id: number
  user_id: number
  username: string
  kind: string
  words: string
  model: string
  request_id: string
  context: string
  created_at: number
}

type VGuardEventDetail = VGuardEventRow & { detail: string }

type DailyStats = {
  date: string
  totals: Record<string, number>
  unique_users: number
  new_blocked_users: number
  top_words: Array<{ k: string; v: number }>
  top_users: Array<{ user_id: number; username: string; count: number }>
  by_model: Array<{ k: string; v: number }>
}

function fmtTime(unix: number) {
  if (!unix) return '-'
  return new Date(unix * 1000).toLocaleString('zh-CN', { hour12: false })
}

function kindLabel(kind: string) {
  if (kind === 'weak') return '弱词命中'
  if (kind === 'strong') return '强词拦截'
  if (kind === 'blocked') return '触发封禁'
  if (kind === 'llm') return '模型审核'
  if (kind === 'unblocked') return '手动解封'
  if (kind === 'output') return '输出命中'
  if (kind === 'upstream_strong') return '上游强违规'
  if (kind === 'upstream_weak') return '上游弱违规'
  return kind
}

export function ViolationGuardSection({ defaultValues }: { defaultValues: Values }) {
  const { t } = useTranslation()
  const updateOption = useSavePolicy()
  const queryClient = useQueryClient()
  const [eventsPage, setEventsPage] = useState(1)
  const [searchInput, setSearchInput] = useState('')
  const [searchUser, setSearchUser] = useState('')
  const [eventsKind, setEventsKind] = useState('all')
  const [detail, setDetail] = useState<VGuardEventDetail | null>(null)
  const [exporting, setExporting] = useState(false)
  const [dailyDate, setDailyDate] = useState(() => new Date().toISOString().slice(0, 10))

  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues,
  })

  useEffect(() => {
    form.reset(defaultValues)
  }, [defaultValues, form])

  useEffect(() => {
    if (!detail) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setDetail(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [detail])

  const dailyQuery = useQuery({
    queryKey: ['vguard-daily', dailyDate],
    queryFn: async () => {
      const res = await api.get(`/api/vguard/daily?date=${dailyDate}`)
      return res.data as { success: boolean; data: DailyStats }
    },
    enabled: !!dailyDate && dailyDate.length === 10,
  })
  const dailyData = dailyQuery.data?.data
  const dailyFetching = dailyQuery.isFetching
  const statusQuery = useQuery({
    queryKey: ['vguard-status'],
    queryFn: async () => {
      const res = await api.get('/api/vguard/status')
      return res.data as { success: boolean; data: VGuardStatus }
    },
    refetchInterval: 30000,
  })

  const searchUserId = /^\d+$/.test(searchUser) ? Number(searchUser) : 0
  const searchUsername = /^\d+$/.test(searchUser) ? '' : searchUser

  const eventsQuery = useQuery({
    queryKey: ['vguard-events', eventsPage, searchUser, eventsKind],
    queryFn: async () => {
      const params = new URLSearchParams({
        p: String(eventsPage),
        page_size: '15',
      })
      if (searchUserId > 0) params.set('user_id', String(searchUserId))
      else if (searchUsername) params.set('username', searchUsername)
      if (eventsKind !== 'all') params.set('kind', eventsKind)
      const res = await api.get(`/api/vguard/events?${params.toString()}`)
      return res.data as {
        success: boolean
        data: { items: VGuardEventRow[]; total: number; page: number }
      }
    },
  })

  function commitSearch() {
    setEventsPage(1)
    setSearchUser(searchInput.trim())
  }

  function clearSearch() {
    setSearchInput('')
    setEventsPage(1)
    setSearchUser('')
    setEventsKind('all')
  }

  async function onSubmit(values: Values) {
    const updates = Object.entries(values).filter(
      ([key, value]) =>
        value !== defaultValues[key as keyof Values]
    )
    try {
      if (updates.length > 0) {
        await updateOption.mutateAsync(
          Object.fromEntries(
            updates.map(([key, value]) => [key, String(value ?? '')])
          )
        )
      }
      void queryClient.invalidateQueries({ queryKey: ['vguard-status'] })
    } catch (error) {
      handleServerError(error)
    }
  }

  async function unblock(userId: number) {
    try {
      const res = await api.post('/api/vguard/unblock', { user_id: userId })
      if (res.data?.success) {
        toast.success(`已解封用户 ${userId}`)
        void queryClient.invalidateQueries({ queryKey: ['vguard-status'] })
        void queryClient.invalidateQueries({ queryKey: ['vguard-events'] })
      } else {
        toast.error('解封失败')
      }
    } catch (error) {
      handleServerError(error)
    }
  }

  async function showDetail(id: number) {
    try {
      const res = await api.get(`/api/vguard/events/${id}`)
      if (res.data?.success) {
        setDetail(res.data.data as VGuardEventDetail)
      } else {
        toast.error('获取详情失败')
      }
    } catch (error) {
      handleServerError(error)
    }
  }

  async function exportEvents() {
    setExporting(true)
    try {
      const res = await api.get('/api/vguard/events/export?limit=5000', {
        responseType: 'blob',
      })
      const url = URL.createObjectURL(
        new Blob([res.data], { type: 'application/json' })
      )
      const a = document.createElement('a')
      a.href = url
      a.download = `vguard_events_${Date.now()}.json`
      a.click()
      URL.revokeObjectURL(url)
      toast.success('导出完成')
    } catch (error) {
      handleServerError(error)
    } finally {
      setExporting(false)
    }
  }

  const status = statusQuery.data?.data
  const events = eventsQuery.data?.data

  let searchedMatch:
    | {
        user_id: number
        blocked: boolean
        blocked_until: number
      }
    | undefined
  if (status && searchUserId > 0) {
    searchedMatch = status.items.find((it) => it.user_id === searchUserId)
  } else if (status && searchUsername) {
    searchedMatch = status.items.find((it) => it.username === searchUsername)
  }
  const blockedUser =
    searchedMatch && searchedMatch.blocked ? searchedMatch : undefined
  const filtered = Boolean(searchUser) || eventsKind !== 'all'
  const emptyEventsRow = (
    <p className='text-muted-foreground text-sm'>
      {filtered ? t('No records match the current filters') : '暂无触发记录'}
    </p>
  )

  return (
    <SettingsSection title='违规防护（分级拦截）'>
      <p className='text-muted-foreground text-sm'>
        只检查每轮请求的「最新用户消息」（历史消息、工具输出、代码上下文不检查）。强词命中：立即拒绝并封禁该用户；弱词命中：计数并注入防护提示词，窗口内累计达到阈值后封禁。管理员账号豁免。
      </p>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={form.formState.isSubmitting}
            saveLabel='保存违规防护设置'
          />
          <SettingsControlGroup>
            <FormField
              control={form.control}
              name='VGuardEnabled'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>启用违规防护</FormLabel>
                    <FormDescription>
                      总开关；关闭时回退为原生全量敏感词行为（若原生开关也关闭则完全不检查）。
                    </FormDescription>
                  </SettingsSwitchContent>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                </SettingsSwitchItem>
              )}
            />
            <SettingsControlChildren>
              <FormField
                control={form.control}
                name='VGuardInjectEnabled'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>触发弱词后注入防护提示词</FormLabel>
                      <FormDescription>
                        命中弱词的用户的后续请求会在 system 前置注入一段防护指令（内容见下方文本框）。
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        disabled={!form.watch('VGuardEnabled')}
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />
            </SettingsControlChildren>
          </SettingsControlGroup>

          <div className='grid gap-4 sm:grid-cols-3'>
            <FormField
              control={form.control}
              name='VGuardWeakThreshold'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>累计阈值（次）</FormLabel>
                  <FormControl>
                    <Input type='number' min={1} {...field} />
                  </FormControl>
                  <FormDescription>窗口内弱词命中达到该次数即封禁</FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='VGuardWindowMinutes'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>计数窗口（分钟）</FormLabel>
                  <FormControl>
                    <Input type='number' min={1} {...field} />
                  </FormControl>
                  <FormDescription>滑动窗口，过期计数自动清零</FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='VGuardBlockHours'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>封禁时长（小时）</FormLabel>
                  <FormControl>
                    <Input type='number' min={1} {...field} />
                  </FormControl>
                  <FormDescription>达到阈值/命中强词后的临时封禁时长</FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>

          <div className='grid gap-4 sm:grid-cols-2'>
            <FormField
              control={form.control}
              name='VGuardApplyGroups'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>生效分组</FormLabel>
                  <FormControl>
                    <Input placeholder='留空=所有分组；如 default,127' {...field} />
                  </FormControl>
                  <FormDescription>
                    逗号分隔的用户分组列表，只有列表内分组会启用防护；留空对全部分组生效。
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='VGuardUserWhitelist'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>用户白名单</FormLabel>
                  <FormControl>
                    <Input placeholder='用户ID或用户名，逗号分隔' {...field} />
                  </FormControl>
                  <FormDescription>
                    白名单用户完全豁免检查（管理员始终豁免）。
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>

          <div className='grid gap-4 sm:grid-cols-2'>
            <FormField
              control={form.control}
              name='VGuardBanTiers'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>封禁阶梯（小时，逗号分隔）</FormLabel>
                  <FormControl>
                    <Input placeholder='4,24,168,720' {...field} />
                  </FormControl>
                  <FormDescription>
                    第 1/2/3/4 次封禁分别取第 1/2/3/4 档，超出取末档。如 4,24,168,720 = 4小时、1天、7天、30天。
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='VGuardEmailThrottlePerDay'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>警告邮件限频（每人每天）</FormLabel>
                  <FormControl>
                    <Input type='number' min={0} {...field} />
                  </FormControl>
                  <FormDescription>0 = 不限制发送数量；仅限制警告邮件，封禁邮件不受限</FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>

          <SettingsControlGroup>
            <SettingsSwitchItem>
              <SettingsSwitchContent>
                <FormLabel>输出侧检测</FormLabel>
                <FormDescription>
                  被标记用户的模型回复也进行弱词扫描计数（防止模型配合输出违规内容）。
                </FormDescription>
              </SettingsSwitchContent>
              <FormControl>
                <Switch
                  disabled={!form.watch('VGuardEnabled')}
                  checked={form.watch('VGuardOutputCheckEnabled')}
                  onCheckedChange={(v) => form.setValue('VGuardOutputCheckEnabled', v)}
                />
              </FormControl>
            </SettingsSwitchItem>
            <SettingsControlChildren>
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>命中违规发警告邮件</FormLabel>
                  <FormDescription>
                    弱词命中时向用户发送警告邮件（含时间/模型/请求编号，不含具体违禁词），告知再犯将封禁。
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    disabled={!form.watch('VGuardEnabled')}
                    checked={form.watch('VGuardWarnEmailEnabled')}
                    onCheckedChange={(v) => form.setValue('VGuardWarnEmailEnabled', v)}
                  />
                </FormControl>
              </SettingsSwitchItem>
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>封禁时发通知邮件</FormLabel>
                  <FormDescription>
                    触发封禁时向用户发送通知邮件，告知封禁时长、解封时间与升级警告。
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    disabled={!form.watch('VGuardEnabled')}
                    checked={form.watch('VGuardBanEmailEnabled')}
                    onCheckedChange={(v) => form.setValue('VGuardBanEmailEnabled', v)}
                  />
                </FormControl>
              </SettingsSwitchItem>
            </SettingsControlChildren>
          </SettingsControlGroup>

          <div className='grid gap-4 sm:grid-cols-2'>
            <FormField
              control={form.control}
              name='VGuardWarnEmailSubject'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>警告邮件主题</FormLabel>
                  <FormControl>
                    <Input {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='VGuardBanEmailSubject'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>封禁邮件主题</FormLabel>
                  <FormControl>
                    <Input {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>

          <div className='grid gap-4 sm:grid-cols-2'>
            <FormField
              control={form.control}
              name='VGuardWarnEmailBody'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>警告邮件正文（占位符：{'{username} {time} {model} {request_id} {hits} {threshold}'}）</FormLabel>
                  <FormControl>
                    <Textarea rows={6} {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='VGuardBanEmailBody'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>封禁邮件正文（占位符：{'{username} {time} {model} {request_id} {hours} {until} {block_count}'}）</FormLabel>
                  <FormControl>
                    <Textarea rows={6} {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>

          <FormField
            control={form.control}
            name='VGuardStrongWords'
            render={({ field }) => (
              <FormItem>
                <FormLabel>强拦截词（每行一个，命中即拦+封禁）</FormLabel>
                <FormControl>
                  <Textarea rows={8} placeholder='每行一个词' {...field} />
                </FormControl>
                <FormDescription>
                  用于无歧义的严重违规（色情写作成品类、制毒制爆、攻击他人、黑产工具等）。英文短词按词边界匹配。
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='VGuardWeakWords'
            render={({ field }) => (
              <FormItem>
                <FormLabel>计数词（每行一个，计数+注入，累计封禁）</FormLabel>
                <FormControl>
                  <Textarea rows={8} placeholder='每行一个词' {...field} />
                </FormControl>
                <FormDescription>
                  显性违规词汇与灰色领域标记词等，单次不拦截，只计数并注入提示词。
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='VGuardInjectPrompt'
            render={({ field }) => (
              <FormItem>
                <FormLabel>注入的防护提示词</FormLabel>
                <FormControl>
                  <Textarea rows={5} {...field} />
                </FormControl>
                <FormDescription>
                  将以 system 角色前置注入到被标记用户的后续请求中。
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        <div className='rounded-lg border p-3'>
          <div className='mb-1 text-sm font-medium'>
            LLM 内容安全审核（与词表叠加；判定不安全与弱词共用累计阈值与阶梯封禁，事件类型=模型审核）
          </div>
          <p className='text-muted-foreground mb-3 text-xs'>
            与本页「请求检查」的原生全量敏感词互补：原生检查整会话全量拦截，VGuard 只看最新消息，模型审核只复核触发请求。
          </p>
          <SettingsControlGroup>
            <FormField
              control={form.control}
              name='VGuardLLMEnabled'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>启用模型审核</FormLabel>
                    <FormDescription>
                      支持内容安全模型（直接判定 unsafe/safe）与决策模型（按提示词判定），调用走下方网关配置并计费。
                    </FormDescription>
                  </SettingsSwitchContent>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                </SettingsSwitchItem>
              )}
            />
            <SettingsControlChildren>
              <FormField
                control={form.control}
                name='VGuardLLMFailOpen'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>模型异常时放行（fail-open）</FormLabel>
                      <FormDescription>
                        关闭后模型超时/异常时拦截当前请求（谨慎，服务商抖动时会影响触发的请求）。
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        disabled={!form.watch('VGuardLLMEnabled')}
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />
            </SettingsControlChildren>
          </SettingsControlGroup>

          <div className='grid gap-4 sm:grid-cols-3'>
            <FormField
              control={form.control}
              name='VGuardLLMProtocol'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>接口协议</FormLabel>
                  <Select
                    items={[
                      { value: 'chat', label: 'chat（OpenAI 兼容）' },
                      { value: 'systemone', label: 'systemone（决策模型）' },
                    ]}
                    value={field.value}
                    onValueChange={field.onChange}
                  >
                    <FormControl>
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent alignItemWithTrigger={false}>
                      <SelectGroup>
                        <SelectItem value='chat'>chat（OpenAI 兼容）</SelectItem>
                        <SelectItem value='systemone'>systemone（决策模型）</SelectItem>
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                  <FormDescription>
                    chat=OpenAI 兼容对话接口；systemone=决策模型结构化判定接口（/v1/systemone，choice/noul 概率判决，亚秒级）
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='VGuardLLMTrigger'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>触发策略</FormLabel>
                  <Select
                    items={[
                      { value: 'weak_word', label: '命中计数词后复核' },
                      { value: 'always', label: '全量送审' },
                      { value: 'off', label: '关闭触发' },
                    ]}
                    value={field.value}
                    onValueChange={field.onChange}
                  >
                    <FormControl>
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent alignItemWithTrigger={false}>
                      <SelectGroup>
                        <SelectItem value='weak_word'>命中计数词后复核（weak_word）</SelectItem>
                        <SelectItem value='always'>全量送审（always）</SelectItem>
                        <SelectItem value='off'>关闭触发（off）</SelectItem>
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                  <FormDescription>
                    weak_word=命中计数词后交给模型复核，省配额；always=所有请求全量送审
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='VGuardLLMAction'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>判定不安全后动作</FormLabel>
                  <Select
                    items={[
                      { value: 'block', label: '拦截当前请求（block）' },
                      { value: 'record', label: '仅记录计数（record）' },
                    ]}
                    value={field.value}
                    onValueChange={field.onChange}
                  >
                    <FormControl>
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent alignItemWithTrigger={false}>
                      <SelectGroup>
                        <SelectItem value='block'>拦截当前请求（block）</SelectItem>
                        <SelectItem value='record'>仅记录计数（record）</SelectItem>
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                  <FormDescription>
                    两种动作都计数（累计会触发阈值封禁）；block 额外拦截当前请求
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>

          <div className='mt-2 grid gap-4 sm:grid-cols-3'>
            <FormField
              control={form.control}
              name='VGuardLLMBaseURL'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>网关地址</FormLabel>
                  <FormControl>
                    <Input placeholder='http://127.0.0.1:3000' {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='VGuardLLMKey'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>API Key</FormLabel>
                  <FormControl>
                    <Input type='password' placeholder='sk-...' {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='VGuardLLMModel'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>审核模型</FormLabel>
                  <FormControl>
                    <Input placeholder='nvidia/nemotron-3.5-content-safety' {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>

          <div className='mt-2 grid gap-4 sm:grid-cols-3'>
            <FormField
              control={form.control}
              name='VGuardLLMSustainHours'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>持续观察（小时）</FormLabel>
                  <FormControl>
                    <Input type='number' min={0} {...field} />
                  </FormControl>
                  <FormDescription>命中后该用户 N 小时内全量过模型；0=关</FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='VGuardLLMTimeoutSec'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>同步超时（秒）</FormLabel>
                  <FormControl>
                    <Input type='number' min={1} max={600} {...field} />
                  </FormControl>
                  <FormDescription>
                    仅在关闭「慢尾转异步补判」时生效的同步总等待
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='VGuardLLMMaxTextChars'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>送审长度上限（字符）</FormLabel>
                  <FormControl>
                    <Input type='number' min={100} step={1000} {...field} />
                  </FormControl>
                  <FormDescription>
                    超出部分截断不再送审；决策模型 2 万字约 8 秒，建议 60000
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>

          <FormField
            control={form.control}
            name='VGuardLLMModels'
            render={({ field }) => (
              <FormItem>
                <FormLabel>审核模型池（负载均衡）</FormLabel>
                <FormControl>
                  <textarea
                    className='flex min-h-[88px] w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-xs placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50'
                    spellCheck={false}
                    placeholder={'模型名[,协议[,模式]]，每行一个\nnvidia/nemotron-3.5-content-safety,chat,safety\nd3-flash,systemone'}
                    {...field}
                  />
                </FormControl>
                <FormDescription>
                  每行一个：模型名,协议(chat/systemone),模式(safety/decision)，协议与模式可省略（跟随上方全局设置）；留空退回单模型模式。网关按各模型实时成功率与延迟自动加权分配，连续失败自动熔断切换、恢复后自动回归
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          {status?.llm?.pool_health && status.llm.pool_health.length > 0 && (
            <div className='overflow-x-auto rounded-md border'>
              <table className='w-full text-sm'>
                <thead>
                  <tr className='border-b bg-muted/50 text-left text-muted-foreground'>
                    <th className='p-2 font-medium'>模型</th>
                    <th className='p-2 font-medium'>协议</th>
                    <th className='p-2 font-medium'>近10分钟成功率</th>
                    <th className='p-2 font-medium'>平均延迟</th>
                    <th className='p-2 font-medium'>熔断剩余</th>
                  </tr>
                </thead>
                <tbody>
                  {status.llm.pool_health.map((m) => (
                    <tr key={m.model} className='border-b last:border-b-0'>
                      <td className='p-2 font-mono text-xs'>{m.model}</td>
                      <td className='p-2'>{m.protocol}</td>
                      <td className='p-2'>{Math.round((m.success_rate ?? 0) * 100)}%</td>
                      <td className='p-2'>{m.avg_ms > 0 ? `${(m.avg_ms / 1000).toFixed(1)}s` : '—'}</td>
                      <td className='p-2'>{m.cooldown_sec > 0 ? `${m.cooldown_sec}s` : '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {form.watch('VGuardLLMProtocol') === 'systemone' && (
            <div className='mt-2 grid gap-4 sm:grid-cols-2'>
              <FormField
                control={form.control}
                name='VGuardLLMSoInstructions'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>判定问题指令（choice）</FormLabel>
                    <FormControl>
                      <Input placeholder='这条用户消息是否违反内容安全规范...' {...field} />
                    </FormControl>
                    <FormDescription>
                      systemone 协议主问题（choice 二选一）的指令；被审文本放 state.message
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='VGuardLLMSoNoulInstruction'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>辅助问题指令（noul）</FormLabel>
                    <FormControl>
                      <Input placeholder='这条用户消息是否违反内容安全规范...' {...field} />
                    </FormControl>
                    <FormDescription>
                      同问双保险：noul 是非题的概率仅记入日志辅助核查，不参与判定
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='VGuardLLMSoUnsafeCriteria'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>unsafe 选项描述</FormLabel>
                    <FormControl>
                      <Input placeholder='消息包含色情露骨内容、制毒制爆、诈骗黑产、暴恐等违规内容。' {...field} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='VGuardLLMSoSafeCriteria'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>safe 选项描述</FormLabel>
                    <FormControl>
                      <Input placeholder='消息是正常无害的日常或教育类内容。' {...field} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>
          )}

          {form.watch('VGuardLLMProtocol') === 'chat' && (
            <div className='mt-2 grid gap-4 sm:grid-cols-2'>
              <FormField
                control={form.control}
                name='VGuardLLMMode'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>模型类型</FormLabel>
                    <Select
                      items={[
                        { value: 'safety', label: '内容安全模型（safety）' },
                        { value: 'decision', label: '决策模型（decision）' },
                      ]}
                      value={field.value}
                      onValueChange={field.onChange}
                    >
                      <FormControl>
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent alignItemWithTrigger={false}>
                        <SelectGroup>
                          <SelectItem value='safety'>内容安全模型（safety）</SelectItem>
                          <SelectItem value='decision'>决策模型（decision）</SelectItem>
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                    <FormDescription>
                      safety=模型直接审文本按 unsafe/safe 判定；decision=按提示词模板判定
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='VGuardLLMPromptTemplate'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>决策提示词模板（decision）</FormLabel>
                    <FormControl>
                      <Input placeholder='留空用默认；支持 {text}' {...field} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>
          )}

          <SettingsControlGroup>
            <FormField
              control={form.control}
              name='VGuardLLMAsyncEnabled'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>慢尾转异步补判</FormLabel>
                    <FormDescription>
                      同步窗口内未返回（上游排队慢尾）时：用户请求立即放行，同一调用转后台继续等待补判；判不安全照样计数/封禁（下一跳生效）。关闭则按上方超时同步等待。
                    </FormDescription>
                  </SettingsSwitchContent>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                </SettingsSwitchItem>
              )}
            />
          </SettingsControlGroup>

          <div className='mt-2 grid gap-4 sm:grid-cols-2'>
            <FormField
              control={form.control}
              name='VGuardLLMFastSeconds'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>同步等待窗口（秒）</FormLabel>
                  <FormControl>
                    <Input type='number' min={1} max={300} {...field} />
                  </FormControl>
                  <FormDescription>
                    送审后最多让用户请求同步等这么久；约96%的调用在此窗口内出结果并当场处理
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='VGuardLLMAsyncSeconds'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>异步补判总预算（秒）</FormLabel>
                  <FormControl>
                    <Input type='number' min={3} max={600} {...field} />
                  </FormControl>
                  <FormDescription>
                    含同步窗口。慢尾调用最多等这么久；超时放弃并记 llm_error:async_timeout
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>

          <div className='mt-2 grid gap-4 sm:grid-cols-3'>
            <FormField
              control={form.control}
              name='VGuardLLMSafeExitStreak'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>连续安全出窗条数</FormLabel>
                  <FormControl>
                    <Input type='number' min={0} max={50} {...field} />
                  </FormControl>
                  <FormDescription>
                    观察窗内连续 N 次判安全 → 剩余观察时间减半；0=关闭递归出窗
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='VGuardLLMSafeExitFloorMin'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>出窗下限（分钟）</FormLabel>
                  <FormControl>
                    <Input type='number' min={0} max={720} {...field} />
                  </FormControl>
                  <FormDescription>
                    减半后剩余不足该分钟数时直接出窗
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='VGuardLLMWeakReenter'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>弱词命中重新进窗</FormLabel>
                  <FormControl>
                    <Checkbox checked={field.value} onCheckedChange={field.onChange} />
                  </FormControl>
                  <FormDescription>
                    开启后弱词命中即把观察窗重新拉满（出窗后再命中再进窗）
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>

          <div className='mt-2 grid gap-4 sm:grid-cols-2'>
            <FormField
              control={form.control}
              name='VGuardLLMMaxConcurrent'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>送审并发上限</FormLabel>
                  <FormControl>
                    <Input type='number' min={0} max={64} {...field} />
                  </FormControl>
                  <FormDescription>
                    全局同时在途送审数（含重试）；0=不限。审核与正常聊天共享上游并发池，建议 2~5
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='VGuardLLMErrorCooldownSec'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>失败事件限噪（秒）</FormLabel>
                  <FormControl>
                    <Input type='number' min={0} max={3600} {...field} />
                  </FormControl>
                  <FormDescription>
                    同一送审失败在窗口内只记 1 条事件，其余合并进下一条；0=不限噪
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>

          {form.watch('VGuardLLMProtocol') === 'chat' && (
          <div className='mt-2 grid gap-4 sm:grid-cols-2'>
            <FormField
              control={form.control}
              name='VGuardLLMUnsafePattern'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>不安全判定正则</FormLabel>
                  <FormControl>
                    <Input {...field} />
                  </FormControl>
                  <FormDescription>先判不安全再判安全（默认覆盖 User Safety: unsafe）</FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='VGuardLLMSafePattern'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>安全判定正则</FormLabel>
                  <FormControl>
                    <Input {...field} />
                  </FormControl>
                  <FormDescription>两则都不匹配视为未知（按 fail-open 策略处理）</FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>
          )}
        </div>

        </SettingsForm>
      </Form>

      <div className='mt-6 space-y-3 rounded-lg border p-3'>
        <div className='flex items-center gap-2'>
          <h3 className='text-sm font-medium'>每日违规摘要</h3>
          <Input
            type='date'
            className='w-40'
            value={dailyDate}
            onChange={(e) => setDailyDate(e.target.value)}
          />
          {dailyFetching && (
            <span className='text-muted-foreground text-xs'>加载中...</span>
          )}
        </div>
        {dailyData ? (
          <div className='space-y-2 text-sm'>
            <div className='text-muted-foreground'>
              {dailyData.date} · 计数 {dailyData.totals.weak ?? 0} · 拦截 {dailyData.totals.strong ?? 0}
              · 输出侧 {dailyData.totals.output ?? 0} · 上游 {((dailyData.totals.upstream_strong ?? 0) + (dailyData.totals.upstream_weak ?? 0))}
              · 封禁 {dailyData.totals.blocked ?? 0} · 涉及用户 {dailyData.unique_users} 人
              · 新增封禁用户 {dailyData.new_blocked_users} 人
            </div>
            {dailyData.top_words?.length > 0 && (
              <div>
                <span className='font-medium'>高频命中词：</span>
                {dailyData.top_words.map((w) => `${w.k}(${w.v})`).join('、')}
              </div>
            )}
            {dailyData.top_users?.length > 0 && (
              <div>
                <span className='font-medium'>活跃违规用户：</span>
                {dailyData.top_users.map((u) => `${u.username || u.user_id}(${u.count})`).join('、')}
              </div>
            )}
            {dailyData.by_model?.length > 0 && (
              <div>
                <span className='font-medium'>模型分布：</span>
                {dailyData.by_model.map((m) => `${m.k || '-'}(${m.v})`).join('、')}
              </div>
            )}
          </div>
        ) : (
          <p className='text-muted-foreground text-sm'>选择日期点击查看</p>
        )}
      </div>

      {detail && (
        <div
          className='fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4'
          onClick={(e) => {
            if (e.target === e.currentTarget) setDetail(null)
          }}
        >
          <div className='max-h-[80vh] w-full max-w-3xl overflow-y-auto rounded-lg bg-background p-4 shadow-lg'>
            <div className='flex items-center justify-between'>
              <h3 className='text-sm font-medium'>
                触发详情 #{detail.id} · {kindLabel(detail.kind)} ·{' '}
                {detail.username || detail.user_id}
              </h3>
              <Button variant='outline' size='sm' onClick={() => setDetail(null)}>
                关闭（ESC）
              </Button>
            </div>
            <div className='text-muted-foreground mt-2 text-sm'>
              时间 {fmtTime(detail.created_at)} · 命中词 {detail.words} · 模型{' '}
              {detail.model} · 请求ID {detail.request_id}
            </div>
            <div className='mt-3 rounded border bg-muted/50 p-2 text-sm'>
              <div className='mb-1 font-medium'>命中上下文</div>
              <div className='whitespace-pre-wrap break-all'>
                {detail.context || '（无）'}
              </div>
            </div>
            <div className='mt-3 rounded border p-2 text-sm'>
              <div className='mb-1 font-medium'>被拦截消息全文</div>
              <div className='max-h-[40vh] overflow-y-auto whitespace-pre-wrap break-all'>
                {detail.detail || '（无）'}
              </div>
            </div>
          </div>
        </div>
      )}

      <div className='mt-6 space-y-4'>
        <div className='flex items-center justify-between'>
          <h3 className='text-sm font-medium'>
            当前状态（每 30 秒自动刷新）
          </h3>
          <div className='flex gap-2'>
            <Button
              variant='outline'
              size='sm'
              disabled={exporting}
              onClick={() => void exportEvents()}
            >
              {exporting ? '导出中...' : '导出日志(JSON)'}
            </Button>
            <Button
              variant='outline'
              size='sm'
              onClick={() => {
                void queryClient.invalidateQueries({
                  queryKey: ['vguard-status'],
                })
                void queryClient.invalidateQueries({
                  queryKey: ['vguard-events'],
                })
              }}
            >
              刷新
            </Button>
          </div>
        </div>
        <div className='text-muted-foreground text-sm'>
          {status
            ? `防护${status.enabled ? '已启用' : '已关闭'} · 强词 ${status.strong_words_n} 个 · 计数词 ${status.weak_words_n} 个 · 生效分组 ${status.apply_groups || '全部'} · 活跃用户 ${status.items.length} 个`
            : '加载中...'}
        </div>
        {status && status.items.length > 0 && (
          <div className='overflow-x-auto'>
            <table className='w-full text-sm'>
              <thead>
                <tr className='border-b text-left'>
                  <th className='p-2'>用户ID</th>
                  <th className='p-2'>用户名</th>
                  <th className='p-2'>弱词命中</th>
                  <th className='p-2'>强词次数</th>
                  <th className='p-2'>状态</th>
                  <th className='p-2'>解封时间</th>
                  <th className='p-2'>操作</th>
                </tr>
              </thead>
              <tbody>
                {status.items.map((it) => (
                  <tr key={it.user_id} className='border-b'>
                    <td className='p-2'>{it.user_id}</td>
                    <td className='p-2'>{it.username || '-'}</td>
                    <td className='p-2'>{it.weak_hits}</td>
                    <td className='p-2'>{it.strong_count}</td>
                    <td className='p-2'>
                      {it.blocked ? '封禁中' : '计数中'}
                    </td>
                    <td className='p-2'>
                      {it.blocked ? fmtTime(it.blocked_until) : '-'}
                    </td>
                    <td className='p-2'>
                      {it.blocked && (
                        <Button
                          variant='outline'
                          size='sm'
                          onClick={() => void unblock(it.user_id)}
                        >
                          解封
                        </Button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        <h3 className='text-sm font-medium'>触发日志（最近记录）</h3>
        <div className='flex flex-wrap items-center gap-2'>
          <Input
            className='max-w-[280px]'
            placeholder={t(
              'Search username or user ID to view the full violation chain'
            )}
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') commitSearch()
            }}
          />
          <Select
            value={eventsKind}
            onValueChange={(v) => {
              setEventsKind(v ?? 'all')
              setEventsPage(1)
            }}
          >
            <SelectTrigger className='w-[150px]'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value='all'>{t('All types')}</SelectItem>
              {['weak', 'strong', 'llm', 'output', 'blocked', 'upstream_strong', 'upstream_weak', 'unblocked'].map((k) => (
                <SelectItem key={k} value={k}>
                  {kindLabel(k)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button variant='outline' size='sm' onClick={commitSearch}>
            {t('Search')}
          </Button>
          <Button variant='outline' size='sm' onClick={clearSearch}>
            {t('Clear')}
          </Button>
        </div>
        {blockedUser && (
          <div className='flex flex-wrap items-center gap-3 rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm'>
            <span>
              {t('User is currently blocked until {{time}}', {
                time: fmtTime(blockedUser.blocked_until),
              })}
            </span>
            <Button
              variant='destructive'
              size='sm'
              onClick={() => void unblock(blockedUser.user_id)}
            >
              {t('Unblock')}
            </Button>
          </div>
        )}
        {events && events.items.length > 0 ? (
          <div className='overflow-x-auto'>
            <table className='w-full text-sm'>
              <thead>
                <tr className='border-b text-left'>
                  <th className='p-2'>时间</th>
                  <th className='p-2'>用户</th>
                  <th className='p-2'>类型</th>
                  <th className='p-2'>命中词</th>
                  <th className='p-2'>命中上下文</th>
                  <th className='p-2'>模型</th>
                  <th className='p-2'>操作</th>
                </tr>
              </thead>
              <tbody>
                {events.items.map((e) => (
                  <tr key={e.id} className='border-b'>
                    <td className='p-2 whitespace-nowrap'>
                      {fmtTime(e.created_at)}
                    </td>
                    <td className='p-2'>
                      {e.username || '-'}({e.user_id})
                    </td>
                    <td className='p-2'>{kindLabel(e.kind)}</td>
                    <td className='p-2'>{e.words}</td>
                    <td className='p-2 max-w-[280px] truncate' title={e.context}>
                      {e.context || '-'}
                    </td>
                    <td className='p-2'>{e.model}</td>
                    <td className='p-2'>
                      <Button
                        variant='outline'
                        size='sm'
                        onClick={() => void showDetail(e.id)}
                      >
                        详情
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <div className='mt-2 flex items-center gap-2'>
              <Button
                variant='outline'
                size='sm'
                disabled={eventsPage <= 1}
                onClick={() => setEventsPage((p) => p - 1)}
              >
                上一页
              </Button>
              <span className='text-sm'>第 {events.page} 页 / 共 {Math.max(1, Math.ceil(events.total / 15))} 页（{events.total} 条）</span>
              <Button
                variant='outline'
                size='sm'
                disabled={eventsPage >= Math.ceil(events.total / 15)}
                onClick={() => setEventsPage((p) => p + 1)}
              >
                下一页
              </Button>
            </div>
          </div>
        ) : (
          emptyEventsRow
        )}
      </div>
    </SettingsSection>
  )
}
