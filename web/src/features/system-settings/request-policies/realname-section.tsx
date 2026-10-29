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
import { toast } from 'sonner'

import { api } from '@/lib/api'
import { Input } from '@/components/ui/input'
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
import type { RealNameSettings } from './defaults'

type ReviewRow = {
  id: number
  user_id: number
  username?: string
  status: string
  method: string
  phone: string
  name_masked: string
  id_no_masked: string
  provider: string
  verified_at: number
  submit_note: string
  review_note: string
  created_at: number
}

function fmtTime(unix: number) {
  if (!unix) return '-'
  return new Date(unix * 1000).toLocaleString('zh-CN', { hour12: false })
}

export function RealNameSection({
  defaultValues,
}: {
  defaultValues: RealNameSettings
}) {
  const queryClient = useQueryClient()
  const updateOption = useSavePolicy()
  const [form, setForm] = useState<RealNameSettings>(defaultValues)
  const [saving, setSaving] = useState(false)
  const [listStatus, setListStatus] = useState('pending')
  const [savingKey, setSavingKey] = useState('')

  useEffect(() => {
    setForm(defaultValues)
  }, [defaultValues])

  const listQuery = useQuery({
    queryKey: ['realname-admin-list', listStatus],
    queryFn: async () => {
      const res = await api.get(
        `/api/realname/admin/list?status=${listStatus}&page_size=50`
      )
      return res.data as {
        success: boolean
        data: { items: ReviewRow[]; total: number; verified_total: number }
      }
    },
  })

  function set<K extends keyof RealNameSettings>(key: K, value: RealNameSettings[K]) {
    setForm((f) => ({ ...f, [key]: value }))
  }

  async function save() {
    setSaving(true)
    try {
      const updates: Record<string, string> = {}
      for (const [key, value] of Object.entries(form)) {
        if (
          value !==
          defaultValues[key as keyof RealNameSettings]
        ) {
          updates[key] = String(value ?? '')
        }
      }
      if (Object.keys(updates).length > 0) {
        await updateOption.mutateAsync(updates)
      }
      toast.success('实名认证设置已保存')
      void queryClient.invalidateQueries({ queryKey: ['realname-admin-list'] })
    } catch (error) {
      handleServerError(error)
    } finally {
      setSaving(false)
    }
  }

  async function review(userId: number, approve: boolean) {
    setSavingKey(`review-${userId}`)
    try {
      const res = await api.post('/api/realname/admin/review', {
        user_id: userId,
        approve,
        note: '',
      })
      if (res.data?.success) {
        toast.success(approve ? '已通过认证' : '已驳回')
        void queryClient.invalidateQueries({
          queryKey: ['realname-admin-list'],
        })
      } else {
        toast.error('操作失败')
      }
    } catch (error) {
      handleServerError(error)
    } finally {
      setSavingKey('')
    }
  }

  const list = listQuery.data?.data

  return (
    <SettingsSection title='实名认证（默认关闭，可全量后台控制）'>
      <p className='text-muted-foreground text-sm'>
        四条路线可切换：A 手机短信（深度合成规定§9 认可）、B 身份证二要素（第三方接口）、C 分级组合（短信起步，充值/用量达标后升二要素）、D 人工审核；国家网证接口已预留。是否拦截调用/充值由下方开关独立控制，关闭时仅前台提醒。
      </p>
      <SettingsForm
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <SettingsPageFormActions
          onSave={() => void save()}
          isSaving={saving}
          saveLabel='保存实名认证设置'
        />
        <SettingsControlGroup>
          <SettingsSwitchItem>
            <SettingsSwitchContent>
              <div className='text-sm font-medium'>启用实名认证</div>
              <div className='text-muted-foreground text-sm'>
                总开关。关闭后所有闸门与前台提醒全部隐藏，不影响已绑定的手机号数据。
              </div>
            </SettingsSwitchContent>
            <Switch
              checked={form.RealNameEnabled}
              onCheckedChange={(v) => set('RealNameEnabled', v)}
            />
          </SettingsSwitchItem>
          <SettingsControlChildren>
            <SettingsSwitchItem>
              <SettingsSwitchContent>
                <div className='text-sm font-medium'>未实名禁止 API 调用</div>
                <div className='text-muted-foreground text-sm'>
                  开启后：作用范围内未达等级的用户调用任何模型将被拒绝；关闭则不拦截只提醒。
                </div>
              </SettingsSwitchContent>
              <Switch
                disabled={!form.RealNameEnabled}
                checked={form.RealNameBlockRelay}
                onCheckedChange={(v) => set('RealNameBlockRelay', v)}
              />
            </SettingsSwitchItem>
            <SettingsSwitchItem>
              <SettingsSwitchContent>
                <div className='text-sm font-medium'>未实名禁止充值</div>
                <div className='text-muted-foreground text-sm'>
                  开启后：未实名用户发起充值时被拒绝（已实名用户不受影响）。
                </div>
              </SettingsSwitchContent>
              <Switch
                disabled={!form.RealNameEnabled}
                checked={form.RealNameBlockTopup}
                onCheckedChange={(v) => set('RealNameBlockTopup', v)}
              />
            </SettingsSwitchItem>
            <SettingsSwitchItem>
              <SettingsSwitchContent>
                <div className='text-sm font-medium'>一证一号</div>
                <div className='text-muted-foreground text-sm'>
                  同一身份证号（加盐哈希比对，不存明文）只能认证一个账号。
                </div>
              </SettingsSwitchContent>
              <Switch
                disabled={!form.RealNameEnabled}
                checked={form.RealNameOneIdOneAccount}
                onCheckedChange={(v) => set('RealNameOneIdOneAccount', v)}
              />
            </SettingsSwitchItem>
          </SettingsControlChildren>
        </SettingsControlGroup>

        <div className='grid gap-4 sm:grid-cols-3'>
          <div className='space-y-1.5'>
            <div className='text-sm font-medium'>认证方式</div>
            <Input
              placeholder='phone / id2 / tiered / manual / national'
              value={form.RealNameMethod}
              onChange={(e) => set('RealNameMethod', e.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              phone=短信(A) id2=二要素(B) tiered=分级(C) manual=人工(D) national=网证(预留)
            </p>
          </div>
          <div className='space-y-1.5'>
            <div className='text-sm font-medium'>生效分组</div>
            <Input
              placeholder='留空=所有分组'
              value={form.RealNameApplyGroups}
              onChange={(e) => set('RealNameApplyGroups', e.target.value)}
            />
            <p className='text-muted-foreground text-xs'>逗号分隔；管理员与白名单始终豁免</p>
          </div>
          <div className='space-y-1.5'>
            <div className='text-sm font-medium'>用户白名单</div>
            <Input
              placeholder='用户ID或用户名，逗号分隔'
              value={form.RealNameUserWhitelist}
              onChange={(e) => set('RealNameUserWhitelist', e.target.value)}
            />
            <p className='text-muted-foreground text-xs'>白名单用户完全不受实名限制</p>
          </div>
        </div>

        <div className='grid gap-4 sm:grid-cols-3'>
          <div className='space-y-1.5'>
            <div className='text-sm font-medium'>C路线·升级触发条件</div>
            <Input
              placeholder='none / topup / quota / always'
              value={form.RealNameTieredId2Trigger}
              onChange={(e) => set('RealNameTieredId2Trigger', e.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              topup=充值过即升级二要素；quota=用量超阈值；always=始终二要素；none=只短信
            </p>
          </div>
          <div className='space-y-1.5'>
            <div className='text-sm font-medium'>C路线·用量阈值(quota)</div>
            <Input
              type='number'
              value={form.RealNameTieredQuotaThreshold}
              onChange={(e) =>
                set('RealNameTieredQuotaThreshold', Number(e.target.value || 0))
              }
            />
            <p className='text-muted-foreground text-xs'>used_quota 超过该值升级二要素（美元×500000）</p>
          </div>
          <div className='space-y-1.5'>
            <div className='text-sm font-medium'>最低年龄（周岁）</div>
            <Input
              type='number'
              value={form.RealNameMinAge}
              onChange={(e) => set('RealNameMinAge', Number(e.target.value || 0))}
            />
            <p className='text-muted-foreground text-xs'>0=不校验；大于 0 按身份证出生日期校验</p>
          </div>
        </div>

        <div className='grid gap-4 sm:grid-cols-2'>
          <div className='space-y-1.5'>
            <div className='text-sm font-medium'>拦截提示语（API 返回）</div>
            <Textarea
              rows={2}
              value={form.RealNameBlockMessage}
              onChange={(e) => set('RealNameBlockMessage', e.target.value)}
            />
          </div>
          <div className='space-y-1.5'>
            <div className='text-sm font-medium'>前台提醒文案（个人资料页）</div>
            <Textarea
              rows={2}
              value={form.RealNameNoticeText}
              onChange={(e) => set('RealNameNoticeText', e.target.value)}
            />
          </div>
        </div>

        <div className='rounded-lg border p-3'>
          <div className='mb-2 text-sm font-medium'>短信服务商（A/C 路线）</div>
          <div className='grid gap-4 sm:grid-cols-3'>
            <div className='space-y-1.5'>
              <div className='text-sm'>服务商</div>
              <Input
                placeholder='aliyun / tencent / generic'
                value={form.RealNameSmsProvider}
                onChange={(e) => set('RealNameSmsProvider', e.target.value)}
              />
              <p className='text-muted-foreground text-xs'>
                阿里云/腾讯云均有免费额度；generic=任意HTTP短信接口
              </p>
            </div>
            <div className='space-y-1.5'>
              <div className='text-sm'>AccessKey / SecretId</div>
              <Input
                value={form.RealNameSmsAccessKey}
                onChange={(e) => set('RealNameSmsAccessKey', e.target.value)}
              />
            </div>
            <div className='space-y-1.5'>
              <div className='text-sm'>SecretKey</div>
              <Input
                type='password'
                value={form.RealNameSmsSecretKey}
                onChange={(e) => set('RealNameSmsSecretKey', e.target.value)}
              />
            </div>
          </div>
          <div className='mt-4 grid gap-4 sm:grid-cols-4'>
            <div className='space-y-1.5'>
              <div className='text-sm'>短信签名</div>
              <Input
                value={form.RealNameSmsSignName}
                onChange={(e) => set('RealNameSmsSignName', e.target.value)}
              />
            </div>
            <div className='space-y-1.5'>
              <div className='text-sm'>模板Code/ID</div>
              <Input
                value={form.RealNameSmsTemplateCode}
                onChange={(e) => set('RealNameSmsTemplateCode', e.target.value)}
              />
              <p className='text-muted-foreground text-xs'>模板变量需含 code</p>
            </div>
            <div className='space-y-1.5'>
              <div className='text-sm'>腾讯 SmsSdkAppId</div>
              <Input
                value={form.RealNameSmsAppId}
                onChange={(e) => set('RealNameSmsAppId', e.target.value)}
              />
            </div>
            <div className='space-y-1.5'>
              <div className='text-sm'>验证码有效期(秒)</div>
              <Input
                type='number'
                value={form.RealNameSmsCodeTTL}
                onChange={(e) => set('RealNameSmsCodeTTL', Number(e.target.value || 300))}
              />
            </div>
          </div>
          <div className='mt-4 grid gap-4 sm:grid-cols-3'>
            <div className='space-y-1.5'>
              <div className='text-sm'>generic·接口地址</div>
              <Input
                value={form.RealNameSmsEndpoint}
                onChange={(e) => set('RealNameSmsEndpoint', e.target.value)}
              />
            </div>
            <div className='space-y-1.5'>
              <div className='text-sm'>generic·Body模板</div>
              <Input
                placeholder='{"phone":"{phone}","code":"{code}"}'
                value={form.RealNameSmsBodyTemplate}
                onChange={(e) => set('RealNameSmsBodyTemplate', e.target.value)}
              />
            </div>
            <div className='space-y-1.5'>
              <div className='text-sm'>generic·Authorization头</div>
              <Input
                value={form.RealNameSmsAuthHeader}
                onChange={(e) => set('RealNameSmsAuthHeader', e.target.value)}
              />
            </div>
          </div>
        </div>

        <div className='rounded-lg border p-3'>
          <div className='mb-2 text-sm font-medium'>身份证二要素服务商（B/C 路线）</div>
          <div className='grid gap-4 sm:grid-cols-3'>
            <div className='space-y-1.5'>
              <div className='text-sm'>服务商</div>
              <Input
                placeholder='aliyun-market / tencent / generic'
                value={form.RealNameId2Provider}
                onChange={(e) => set('RealNameId2Provider', e.target.value)}
              />
              <p className='text-muted-foreground text-xs'>
                阿里云市场APPCODE个人可购约0.2元/次（推荐起步）；腾讯云需企业
              </p>
            </div>
            <div className='space-y-1.5'>
              <div className='text-sm'>APPCODE / SecretId</div>
              <Input
                value={form.RealNameId2AppCode}
                onChange={(e) => set('RealNameId2AppCode', e.target.value)}
              />
            </div>
            <div className='space-y-1.5'>
              <div className='text-sm'>SecretKey（腾讯云）</div>
              <Input
                type='password'
                value={form.RealNameId2SecretKey}
                onChange={(e) => set('RealNameId2SecretKey', e.target.value)}
              />
            </div>
          </div>
          <div className='mt-4 grid gap-4 sm:grid-cols-2'>
            <div className='space-y-1.5'>
              <div className='text-sm'>接口地址（支持占位符）</div>
              <Input
                placeholder='https://xxxx.aliyuncs.com/xxx?name={name}&idNo={idno}'
                value={form.RealNameId2Endpoint}
                onChange={(e) => set('RealNameId2Endpoint', e.target.value)}
              />
            </div>
            <div className='space-y-1.5'>
              <div className='text-sm'>POST Body 模板（generic 可选）</div>
              <Input
                placeholder='{"name":"{name}","idNo":"{idno}"}'
                value={form.RealNameId2BodyTemplate}
                onChange={(e) => set('RealNameId2BodyTemplate', e.target.value)}
              />
            </div>
          </div>
          <div className='mt-4 grid gap-4 sm:grid-cols-2'>
            <div className='space-y-1.5'>
              <div className='text-sm'>成功判定规则（可选JSON）</div>
              <Input
                placeholder='{"code_field":"code","code_ok":["0","200"],"msg_field":"msg"}'
                value={form.RealNameId2SuccessRule}
                onChange={(e) => set('RealNameId2SuccessRule', e.target.value)}
              />
              <p className='text-muted-foreground text-xs'>
                不填则自动识别 code/status/errcode=0或200、data.isVerify=1、Response.Result=0
              </p>
            </div>
            <div className='space-y-1.5'>
              <div className='text-sm'>手机号正则</div>
              <Input
                value={form.RealNamePhonePattern}
                onChange={(e) => set('RealNamePhonePattern', e.target.value)}
              />
            </div>
          </div>
        </div>
      </SettingsForm>

      <div className='mt-6 space-y-3 rounded-lg border p-3'>
        <div className='flex items-center gap-2'>
          <h3 className='text-sm font-medium'>认证记录 / 人工审核</h3>
          <Input
            className='w-40'
            placeholder='状态筛选'
            value={listStatus}
            onChange={(e) => setListStatus(e.target.value)}
          />
          <Button
            variant='outline'
            size='sm'
            onClick={() =>
              void queryClient.invalidateQueries({
                queryKey: ['realname-admin-list'],
              })
            }
          >
            刷新
          </Button>
        </div>
        {list ? (
          <div className='text-muted-foreground text-sm'>
            已实名通过 {list.verified_total} 人 · 当前筛选 {list.total} 条
          </div>
        ) : (
          <p className='text-muted-foreground text-sm'>加载中...</p>
        )}
        {list && list.items.length > 0 && (
          <div className='overflow-x-auto'>
            <table className='w-full text-sm'>
              <thead>
                <tr className='border-b text-left'>
                  <th className='p-2'>用户ID</th>
                  <th className='p-2'>状态</th>
                  <th className='p-2'>方式</th>
                  <th className='p-2'>手机号</th>
                  <th className='p-2'>姓名</th>
                  <th className='p-2'>身份证</th>
                  <th className='p-2'>提交说明</th>
                  <th className='p-2'>时间</th>
                  <th className='p-2'>操作</th>
                </tr>
              </thead>
              <tbody>
                {list.items.map((r) => (
                  <tr key={r.id} className='border-b'>
                    <td className='p-2'>{r.user_id}</td>
                    <td className='p-2'>
                      {r.status === 'verified'
                        ? '已通过'
                        : r.status === 'pending'
                          ? '待审核'
                          : r.status === 'rejected'
                            ? '已驳回'
                            : '未认证'}
                    </td>
                    <td className='p-2'>{r.method || '-'}</td>
                    <td className='p-2'>{r.phone || '-'}</td>
                    <td className='p-2'>{r.name_masked || '-'}</td>
                    <td className='p-2'>{r.id_no_masked || '-'}</td>
                    <td className='p-2 max-w-[200px] truncate' title={r.submit_note}>
                      {r.submit_note || '-'}
                    </td>
                    <td className='p-2 whitespace-nowrap'>{fmtTime(r.created_at)}</td>
                    <td className='p-2'>
                      {r.status === 'pending' && (
                        <div className='flex gap-1'>
                          <Button
                            size='sm'
                            disabled={savingKey === `review-${r.user_id}`}
                            onClick={() => void review(r.user_id, true)}
                          >
                            通过
                          </Button>
                          <Button
                            size='sm'
                            variant='outline'
                            disabled={savingKey === `review-${r.user_id}`}
                            onClick={() => void review(r.user_id, false)}
                          >
                            驳回
                          </Button>
                        </div>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </SettingsSection>
  )
}
