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
import { useState } from 'react'
import { toast } from 'sonner'

import { api } from '@/lib/api'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { handleServerError } from '@/lib/handle-server-error'

type RealNameStatus = {
  enabled: boolean
  method: string
  need_level: number
  achieved_level: number
  verified: boolean
  status: string
  phone_bound: boolean
  phone_masked: string
  name_masked: string
  id_no_masked: string
  provider: string
  verified_at: number
  review_note: string
  notice_text: string
  block_relay: boolean
  block_topup: boolean
}

function fmtTime(unix: number) {
  if (!unix) return '-'
  return new Date(unix * 1000).toLocaleString('zh-CN', { hour12: false })
}

function methodLabel(m: string) {
  if (m === 'phone') return '手机号实名'
  if (m === 'id2') return '身份证二要素'
  if (m === 'manual') return '人工审核'
  if (m === 'national') return '国家网络身份认证'
  return m
}

export function RealNameCard() {
  const queryClient = useQueryClient()
  const [phone, setPhone] = useState('')
  const [code, setCode] = useState('')
  const [realName, setRealName] = useState('')
  const [idNo, setIdNo] = useState('')
  const [note, setNote] = useState('')
  const [countdown, setCountdown] = useState(0)
  const [busy, setBusy] = useState('')

  const statusQuery = useQuery({
    queryKey: ['realname-status'],
    queryFn: async () => {
      const res = await api.get('/api/user/realname/status')
      return res.data as { success: boolean; data: RealNameStatus }
    },
  })
  const st = statusQuery.data?.data
  if (!st || !st.enabled) {
    return null
  }

  function refresh() {
    void queryClient.invalidateQueries({ queryKey: ['realname-status'] })
  }

  async function sendCode() {
    if (!phone.trim()) {
      toast.error('请输入手机号')
      return
    }
    setBusy('send')
    try {
      const res = await api.post('/api/user/realname/phone/send', {
        phone: phone.trim(),
      })
      if (res.data?.success) {
        toast.success('验证码已发送，请查收短信')
        setCountdown(60)
        const timer = setInterval(() => {
          setCountdown((c) => {
            if (c <= 1) {
              clearInterval(timer)
              return 0
            }
            return c - 1
          })
        }, 1000)
      } else {
        toast.error(res.data?.message || '发送失败')
      }
    } catch (error) {
      handleServerError(error)
    } finally {
      setBusy('')
    }
  }

  async function bindPhone() {
    if (!phone.trim() || !code.trim()) {
      toast.error('请输入手机号与验证码')
      return
    }
    setBusy('bind')
    try {
      const res = await api.post('/api/user/realname/phone/bind', {
        phone: phone.trim(),
        code: code.trim(),
      })
      if (res.data?.success) {
        toast.success('手机号绑定成功')
        setCode('')
        refresh()
      } else {
        toast.error(res.data?.message || '绑定失败')
      }
    } catch (error) {
      handleServerError(error)
    } finally {
      setBusy('')
    }
  }

  async function submitId2() {
    if (!realName.trim() || !idNo.trim()) {
      toast.error('请输入姓名与身份证号')
      return
    }
    setBusy('id2')
    try {
      const res = await api.post('/api/user/realname/id2', {
        real_name: realName.trim(),
        id_no: idNo.trim(),
      })
      if (res.data?.success) {
        toast.success('实名认证通过')
        setIdNo('')
        refresh()
      } else {
        toast.error(res.data?.message || '认证未通过')
      }
    } catch (error) {
      handleServerError(error)
    } finally {
      setBusy('')
    }
  }

  async function submitManual() {
    if (!realName.trim() || !idNo.trim()) {
      toast.error('请输入姓名与身份证号')
      return
    }
    setBusy('manual')
    try {
      const res = await api.post('/api/user/realname/manual', {
        real_name: realName.trim(),
        id_no: idNo.trim(),
        note: note.trim(),
      })
      if (res.data?.success) {
        toast.success('已提交，等待管理员审核')
        setIdNo('')
        refresh()
      } else {
        toast.error(res.data?.message || '提交失败')
      }
    } catch (error) {
      handleServerError(error)
    } finally {
      setBusy('')
    }
  }

  const needPhone = st.achieved_level < 1
  const needLevel2 =
    st.need_level >= 2 && st.achieved_level < 2 && st.status !== 'pending'

  return (
    <div className='rounded-lg border bg-card p-4 shadow-sm'>
      <div className='mb-2 flex items-center justify-between'>
        <h3 className='text-sm font-semibold'>实名认证</h3>
        {st.verified ? (
          <span className='rounded bg-emerald-100 px-2 py-0.5 text-xs font-medium text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300'>
            已认证 · {methodLabel(st.method)}
          </span>
        ) : (
          <span className='rounded bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-950 dark:text-amber-300'>
            未认证
          </span>
        )}
      </div>

      {st.verified ? (
        <div className='space-y-1 text-sm'>
          <div className='text-muted-foreground'>
            {st.name_masked && <span>姓名 {st.name_masked} · </span>}
            {st.id_no_masked && <span>身份证 {st.id_no_masked} · </span>}
            认证时间 {fmtTime(st.verified_at)}
          </div>
          {st.phone_masked && (
            <div className='text-muted-foreground'>绑定手机 {st.phone_masked}</div>
          )}
        </div>
      ) : (
        <div className='space-y-3'>
          <div className='rounded border border-amber-300/60 bg-amber-50 p-2 text-xs text-amber-800 dark:bg-amber-950/50 dark:text-amber-200'>
            {st.notice_text ||
              '根据国家相关规定，使用 AI 服务需完成实名认证。'}
            {(st.block_relay || st.block_topup) && (
              <span className='mt-1 block font-medium'>
                未完成实名将无法{st.block_relay ? '调用 API' : ''}
                {st.block_relay && st.block_topup ? ' / ' : ''}
                {st.block_topup ? '充值' : ''}。
              </span>
            )}
          </div>

          {needPhone && (
            <div className='space-y-2'>
              <div className='text-sm font-medium'>
                第一步：绑定手机号{st.need_level >= 2 ? '（基础实名）' : '（完成实名）'}
              </div>
              <div className='flex gap-2'>
                <Input
                  className='flex-1'
                  placeholder='手机号'
                  value={phone}
                  onChange={(e) => setPhone(e.target.value)}
                />
                <Button
                  variant='outline'
                  size='sm'
                  disabled={countdown > 0 || busy === 'send'}
                  onClick={() => void sendCode()}
                >
                  {countdown > 0 ? `${countdown}s` : busy === 'send' ? '发送中' : '获取验证码'}
                </Button>
              </div>
              <div className='flex gap-2'>
                <Input
                  className='flex-1'
                  placeholder='短信验证码'
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                />
                <Button size='sm' disabled={busy === 'bind'} onClick={() => void bindPhone()}>
                  {busy === 'bind' ? '绑定中' : '绑定'}
                </Button>
              </div>
            </div>
          )}

          {needLevel2 && (
            <div className='space-y-2'>
              <div className='text-sm font-medium'>
                第二步：{st.method === 'manual' ? '提交人工审核' : '身份证实名认证'}
              </div>
              <Input
                placeholder='真实姓名'
                value={realName}
                onChange={(e) => setRealName(e.target.value)}
              />
              <Input
                placeholder='身份证号（仅用于核验，平台不存储明文）'
                value={idNo}
                onChange={(e) => setIdNo(e.target.value)}
              />
              {st.method === 'manual' && (
                <Input
                  placeholder='补充说明（可选）'
                  value={note}
                  onChange={(e) => setNote(e.target.value)}
                />
              )}
              <Button
                size='sm'
                disabled={busy === 'id2' || busy === 'manual'}
                onClick={() =>
                  st.method === 'manual' ? void submitManual() : void submitId2()
                }
              >
                {busy === 'id2' || busy === 'manual'
                  ? '提交中'
                  : st.method === 'manual'
                    ? '提交审核'
                    : '立即认证'}
              </Button>
            </div>
          )}

          {st.status === 'pending' && (
            <div className='rounded border bg-muted/50 p-2 text-sm'>
              您的实名信息已提交，等待管理员审核。{' '}
              {st.review_note && <span className='text-muted-foreground'>{st.review_note}</span>}
            </div>
          )}
          {st.status === 'rejected' && (
            <div className='rounded border border-red-300 bg-red-50 p-2 text-sm text-red-700 dark:bg-red-950/50 dark:text-red-300'>
              认证被驳回{st.review_note ? `：${st.review_note}` : ''}，请重新提交。
            </div>
          )}

          {st.phone_bound && (
            <div className='text-muted-foreground text-xs'>
              已绑定手机 {st.phone_masked || '(已验证)'}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
