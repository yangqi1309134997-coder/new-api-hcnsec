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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMemo, useEffect, useRef } from 'react'
import { useFieldArray, useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'

import { MultiSelect } from '@/components/multi-select'
import { Button } from '@/components/ui/button'
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
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Switch } from '@/components/ui/switch'
import { handleServerError } from '@/lib/handle-server-error'

import {
  SettingsControlChildren,
  SettingsControlGroup,
  SettingsForm,
  SettingsFormGrid,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useResetForm } from '../hooks/use-reset-form'
import type { ChannelDispatchSettings } from './defaults'
import { useSavePolicy } from './use-save-policy'

const STRATEGY_VALUES = [
  'weighted-random',
  'least-connections',
  'random',
  'round-robin',
  'smart',
] as const
type StrategyValue = (typeof STRATEGY_VALUES)[number]

const LOAD_BALANCE_OPTIONS_KEY = 'ChannelLoadBalanceStrategy'

type DispatchFormValues = z.output<ReturnType<typeof createDispatchSchema>>
type DispatchFormInput = z.input<ReturnType<typeof createDispatchSchema>>

type NormalizedDispatchValues = {
  ChannelLoadBalanceStrategy: StrategyValue
  byGroupJson: string
  'loadbalance_setting.smart_top_n': string
  'loadbalance_setting.smart_explore_percent': string
  'loadbalance_setting.smart_fail_cooldown_seconds': string
  'loadbalance_setting.smart_min_samples': string
  'loadbalance_setting.smart_load_penalty': string
  'loadbalance_setting.smart_latency_half_life_minutes': string
  'auto_model_setting.enabled': boolean
  'auto_model_setting.pool': string[]
}

type GroupOverrideRow = { group: string; strategy: StrategyValue }

function parseByGroup(raw: string | undefined): GroupOverrideRow[] {
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return []
    }
    return Object.entries(parsed as Record<string, unknown>)
      .filter(
        (entry): entry is [string, string] => typeof entry[1] === 'string'
      )
      .map(([group, strategy]) => ({
        group,
        strategy: (STRATEGY_VALUES as readonly string[]).includes(strategy)
          ? (strategy as StrategyValue)
          : 'weighted-random',
      }))
  } catch {
    return []
  }
}

function parsePool(pool: string[] | undefined): string[] {
  return (pool ?? []).filter((model) => model.length > 0)
}

function createDispatchSchema(t: (key: string) => string) {
  const strategySchema = z.enum(STRATEGY_VALUES)
  const boundedNumber = (min: number, max: number) =>
    z
      .string()
      .refine((value) => {
        const trimmed = value.trim()
        if (trimmed === '') return false
        const parsed = Number(trimmed)
        return Number.isFinite(parsed) && parsed >= min && parsed <= max
      }, t('Value is out of range'))
  return z
    .object({
      ChannelLoadBalanceStrategy: strategySchema,
      byGroupRows: z
        .array(
          z.object({
            group: z.string().trim().min(1, t('Group name is required')),
            strategy: strategySchema,
          })
        )
        .superRefine((rows, ctx) => {
          const seen = new Set<string>()
          rows.forEach((row, index) => {
            if (seen.has(row.group)) {
              ctx.addIssue({
                code: 'custom',
                path: [index, 'group'],
                message: t('Duplicate group overrides'),
              })
            }
            seen.add(row.group)
          })
        }),
      loadbalance_setting: z.object({
        smart_top_n: boundedNumber(1, 64),
        smart_explore_percent: boundedNumber(0, 50),
        smart_fail_cooldown_seconds: boundedNumber(0, 600),
        smart_min_samples: boundedNumber(1, 1000),
        smart_load_penalty: boundedNumber(0, 100),
        smart_latency_half_life_minutes: boundedNumber(0.5, 1440),
      }),
      auto_model_setting: z.object({
        enabled: z.boolean(),
        pool: z.array(z.string().trim().min(1)),
      }),
    })
    .superRefine((values, ctx) => {
      if (
        values.auto_model_setting.enabled &&
        values.auto_model_setting.pool.length === 0
      ) {
        ctx.addIssue({
          code: 'custom',
          path: ['auto_model_setting.pool'],
          message: t('Candidate model pool cannot be empty when enabled'),
        })
      }
    })
}

const buildFormDefaults = (
  defaults: ChannelDispatchSettings
): DispatchFormInput => ({
  ChannelLoadBalanceStrategy:
    (STRATEGY_VALUES as readonly string[]).includes(
      defaults.ChannelLoadBalanceStrategy
    ) || defaults.ChannelLoadBalanceStrategy === ''
      ? ((defaults.ChannelLoadBalanceStrategy ||
          'weighted-random') as DispatchFormInput['ChannelLoadBalanceStrategy'])
      : 'weighted-random',
  byGroupRows: parseByGroup(defaults['loadbalance_setting.by_group']),
  loadbalance_setting: {
    smart_top_n: String(defaults['loadbalance_setting.smart_top_n']),
    smart_explore_percent: String(
      defaults['loadbalance_setting.smart_explore_percent']
    ),
    smart_fail_cooldown_seconds: String(
      defaults['loadbalance_setting.smart_fail_cooldown_seconds']
    ),
    smart_min_samples: String(defaults['loadbalance_setting.smart_min_samples']),
    smart_load_penalty: String(defaults['loadbalance_setting.smart_load_penalty']),
    smart_latency_half_life_minutes: String(
      defaults['loadbalance_setting.smart_latency_half_life_minutes']
    ),
  },
  auto_model_setting: {
    enabled: defaults['auto_model_setting.enabled'],
    pool: parsePool(defaults['auto_model_setting.pool']),
  },
})

const normalizeDefaults = (
  values: DispatchFormValues
): NormalizedDispatchValues => ({
  ChannelLoadBalanceStrategy: values.ChannelLoadBalanceStrategy,
  byGroupJson: JSON.stringify(
    Object.fromEntries(
      values.byGroupRows.map((row) => [row.group.trim(), row.strategy])
    )
  ),
  'loadbalance_setting.smart_top_n':
    values.loadbalance_setting.smart_top_n.trim(),
  'loadbalance_setting.smart_explore_percent':
    values.loadbalance_setting.smart_explore_percent.trim(),
  'loadbalance_setting.smart_fail_cooldown_seconds':
    values.loadbalance_setting.smart_fail_cooldown_seconds.trim(),
  'loadbalance_setting.smart_min_samples':
    values.loadbalance_setting.smart_min_samples.trim(),
  'loadbalance_setting.smart_load_penalty':
    values.loadbalance_setting.smart_load_penalty.trim(),
  'loadbalance_setting.smart_latency_half_life_minutes':
    values.loadbalance_setting.smart_latency_half_life_minutes.trim(),
  'auto_model_setting.enabled': values.auto_model_setting.enabled,
  'auto_model_setting.pool': parsePool(values.auto_model_setting.pool),
})

export function ChannelDispatchSection({
  defaultValues,
}: {
  defaultValues: ChannelDispatchSettings
}) {
  const { t } = useTranslation()
  const updateOption = useSavePolicy()
  const dispatchSchema = useMemo(() => createDispatchSchema(t), [t])
  const formDefaults = useMemo(
    () => buildFormDefaults(defaultValues),
    [defaultValues]
  )
  const baselineRef = useRef<string>('')

  const form = useForm<DispatchFormInput, unknown, DispatchFormValues>({
    resolver: zodResolver(dispatchSchema),
    defaultValues: formDefaults,
  })
  const byGroupArray = useFieldArray({ control: form.control, name: 'byGroupRows' })
  const autoModelEnabled = form.watch('auto_model_setting.enabled')

  useResetForm(form, formDefaults)
  useEffect(() => {
    baselineRef.current = JSON.stringify(
      normalizeDefaults(formDefaults as DispatchFormValues)
    )
  }, [formDefaults])

  const onSubmit = async (values: DispatchFormValues) => {
    const normalized = normalizeDefaults(values)
    if (JSON.stringify(normalized) === baselineRef.current) {
      toast.info(t('No changes to save'))
      return
    }
    const updates: Record<string, string> = {
      [LOAD_BALANCE_OPTIONS_KEY]: normalized.ChannelLoadBalanceStrategy,
      'loadbalance_setting.by_group': normalized.byGroupJson,
      'loadbalance_setting.smart_top_n': String(
        normalized['loadbalance_setting.smart_top_n']
      ),
      'loadbalance_setting.smart_explore_percent': String(
        normalized['loadbalance_setting.smart_explore_percent']
      ),
      'loadbalance_setting.smart_fail_cooldown_seconds': String(
        normalized['loadbalance_setting.smart_fail_cooldown_seconds']
      ),
      'loadbalance_setting.smart_min_samples': String(
        normalized['loadbalance_setting.smart_min_samples']
      ),
      'loadbalance_setting.smart_load_penalty': String(
        normalized['loadbalance_setting.smart_load_penalty']
      ),
      'loadbalance_setting.smart_latency_half_life_minutes': String(
        normalized['loadbalance_setting.smart_latency_half_life_minutes']
      ),
      'auto_model_setting.enabled': String(
        normalized['auto_model_setting.enabled']
      ),
      'auto_model_setting.pool': JSON.stringify(
        normalized['auto_model_setting.pool']
      ),
    }
    try {
      await updateOption.mutateAsync(updates)
      baselineRef.current = JSON.stringify(normalized)
    } catch (error) {
      handleServerError(error)
    }
  }

  const strategyItems = STRATEGY_VALUES.map((value) => ({
    value,
    label: t(`Strategy ${value}`),
  }))

  return (
    <SettingsSection title={t('Channel dispatch')}>
      <div className='text-muted-foreground space-y-1 text-sm'>
        <p>
          {t(
            'Controls how the gateway picks a channel for each request. Changes take effect after saving without restart.'
          )}
        </p>
      </div>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={form.formState.isSubmitting}
          />

          <h4 className='text-sm font-medium'>
            {t('Load balancing strategy')}
          </h4>
          <SettingsFormGrid>
            <SettingsControlGroup>
              <FormField
                control={form.control}
                name='ChannelLoadBalanceStrategy'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Global default strategy')}</FormLabel>
                    <FormDescription>
                      {t(
                        'Used by groups without a per-group override. Smart routing ignores channel priority and weight and picks by measured health.'
                      )}
                    </FormDescription>
                    <Select
                      items={strategyItems}
                      value={field.value}
                      onValueChange={field.onChange}
                    >
                      <FormControl>
                        <SelectTrigger className='w-full'>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent alignItemWithTrigger={false}>
                        <SelectGroup>
                          {strategyItems.map((item) => (
                            <SelectItem key={item.value} value={item.value}>
                              {item.label}
                            </SelectItem>
                          ))}
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SettingsControlGroup>
          </SettingsFormGrid>

          <Separator />

          <div className='flex min-w-0 flex-col gap-4'>
            <div>
              <h4 className='text-sm font-medium'>
                {t('Per-group strategy overrides')}
              </h4>
              <p className='text-muted-foreground mt-1 text-sm'>
                {t(
                  'Groups listed here use their own strategy; unlisted groups follow the global default.'
                )}
              </p>
            </div>
            {byGroupArray.fields.length === 0 && (
              <p className='text-muted-foreground text-sm'>
                {t('No per-group overrides.')}
              </p>
            )}
            {byGroupArray.fields.map((field, index) => (
              <div
                key={field.id}
                className='grid items-start gap-x-5 gap-y-2 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]'
              >
                <FormField
                  control={form.control}
                  name={`byGroupRows.${index}.group` as const}
                  render={({ field: rowField }) => (
                    <FormItem>
                      <FormLabel>{t('Group name')}</FormLabel>
                      <FormControl>
                        <Input
                          {...rowField}
                          placeholder={t('Group name')}
                          autoComplete='off'
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name={`byGroupRows.${index}.strategy` as const}
                  render={({ field: rowField }) => (
                    <FormItem>
                      <FormLabel>{t('Strategy')}</FormLabel>
                      <Select
                        items={strategyItems}
                        value={rowField.value}
                        onValueChange={rowField.onChange}
                      >
                        <FormControl>
                          <SelectTrigger className='w-full'>
                            <SelectValue />
                          </SelectTrigger>
                        </FormControl>
                        <SelectContent alignItemWithTrigger={false}>
                          <SelectGroup>
                            {strategyItems.map((item) => (
                              <SelectItem key={item.value} value={item.value}>
                                {item.label}
                              </SelectItem>
                            ))}
                          </SelectGroup>
                        </SelectContent>
                      </Select>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <Button
                  type='button'
                  variant='outline'
                  className='mt-6'
                  onClick={() => byGroupArray.remove(index)}
                >
                  {t('Remove')}
                </Button>
              </div>
            ))}
            <div>
              <Button
                type='button'
                variant='outline'
                onClick={() =>
                  byGroupArray.append({ group: '', strategy: 'weighted-random' })
                }
              >
                {t('Add group override')}
              </Button>
            </div>
          </div>

          <Separator />

          <div className='flex min-w-0 flex-col gap-4'>
            <div>
              <h4 className='text-sm font-medium'>
                {t('Smart routing parameters')}
              </h4>
              <p className='text-muted-foreground mt-1 text-sm'>
                {t(
                  'Health samples are per channel: streaming first-token latency, success rate and live concurrency. A short cooldown after failures prevents healthy channels from being flooded.'
                )}
              </p>
            </div>
            <SettingsFormGrid>
              <SettingsControlChildren
                role='group'
                aria-label={t('Smart routing parameters')}
                className='grid gap-x-5 gap-y-4 lg:grid-cols-2'
              >
                <FormField
                  control={form.control}
                  name='loadbalance_setting.smart_top_n'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Top-N tier size')}</FormLabel>
                      <FormDescription>
                        {t(
                          'Pick within the top N channels by score instead of always the single best, so traffic stays spread.'
                        )}
                      </FormDescription>
                      <FormControl>
                        <Input type='number' min={1} max={64} {...field} />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='loadbalance_setting.smart_explore_percent'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Exploration rate (%)')}</FormLabel>
                      <FormDescription>
                        {t(
                          'Share of requests routed randomly so new or idle channels get sampled.'
                        )}
                      </FormDescription>
                      <FormControl>
                        <Input type='number' min={0} max={50} {...field} />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='loadbalance_setting.smart_fail_cooldown_seconds'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Failure cooldown (seconds)')}</FormLabel>
                      <FormDescription>
                        {t(
                          'A channel that just failed is down-weighted for this long before it can take full traffic again.'
                        )}
                      </FormDescription>
                      <FormControl>
                        <Input type='number' min={0} max={600} {...field} />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='loadbalance_setting.smart_min_samples'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Min samples for confidence')}</FormLabel>
                      <FormDescription>
                        {t(
                          'Below this many recent samples the health score is pulled toward neutral, so one fast request cannot put a channel on top.'
                        )}
                      </FormDescription>
                      <FormControl>
                        <Input type='number' min={1} max={1000} {...field} />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='loadbalance_setting.smart_load_penalty'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Load penalty factor')}</FormLabel>
                      <FormDescription>
                        {t(
                          'Higher values divert traffic away from busy channels faster. 0 disables live concurrency feedback.'
                        )}
                      </FormDescription>
                      <FormControl>
                        <Input type='number' min={0} max={100} step={0.1} {...field} />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='loadbalance_setting.smart_latency_half_life_minutes'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Latency half-life (minutes)')}</FormLabel>
                      <FormDescription>
                        {t(
                          'How quickly older health samples lose weight. Larger values remember longer history.'
                        )}
                      </FormDescription>
                      <FormControl>
                        <Input type='number' min={0.5} max={1440} {...field} />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </SettingsControlChildren>
            </SettingsFormGrid>
          </div>

          <Separator />

          <div className='flex min-w-0 flex-col gap-4'>
            <h4 className='text-sm font-medium'>{t('Auto smart model')}</h4>
            <FormField
              control={form.control}
              name='auto_model_setting.enabled'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>{t('Enable auto smart model')}</FormLabel>
                    <FormDescription>
                      {t(
                        'Requests for the auto model are rewritten to the healthiest candidate model in the pool. Billing stays on the auto model ratio; the real model is recorded in log admin info.'
                      )}
                    </FormDescription>
                  </SettingsSwitchContent>
                  <FormControl>
                    <Switch checked={field.value} onCheckedChange={field.onChange} />
                  </FormControl>
                </SettingsSwitchItem>
              )}
            />
            {autoModelEnabled && (
              <FormField
                control={form.control}
                name='auto_model_setting.pool'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Candidate model pool')}</FormLabel>
                    <FormDescription>
                      {t(
                        'Only chat models should be listed. Models missing from the target group are skipped automatically.'
                      )}
                    </FormDescription>
                    <FormControl>
                      <MultiSelect
                        options={field.value.map((model) => ({
                          label: model,
                          value: model,
                        }))}
                        selected={field.value}
                        onChange={field.onChange}
                        allowCreate
                        placeholder={t('Type a model name and press Enter')}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}
          </div>
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
