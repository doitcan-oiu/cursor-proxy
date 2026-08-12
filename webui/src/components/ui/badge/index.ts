import type { VariantProps } from 'class-variance-authority'
import { cva } from 'class-variance-authority'

export { default as Badge } from './Badge.vue'

/*
 * 徽章遵循 DESIGN.md：药丸形、13px/600 字重，语义色成对（浅底 + 深字）。
 * code 变体是唯一的方角（rounded-sm），用于行内代码风格标签。
 */
export const badgeVariants = cva(
  'inline-flex items-center gap-1 whitespace-nowrap rounded-full px-2.5 py-[3px] text-[12px] font-semibold leading-[1.5] [&_svg]:size-3 [&_svg]:shrink-0',
  {
    variants: {
      variant: {
        neutral: 'bg-surface text-steel',
        success: 'bg-success-bg text-success-text',
        danger: 'bg-danger-bg text-danger',
        warn: 'bg-warn-bg text-warn-text',
        new: 'bg-brand-coral text-white',
        beta: 'bg-brand-blue-200 text-brand-blue-deep',
        outline: 'border border-hairline bg-canvas text-steel',
        code: 'rounded-sm bg-brand-blue-200 px-1.5 py-0.5 text-brand-blue-deep font-mono',
      },
    },
    defaultVariants: {
      variant: 'neutral',
    },
  },
)

export type BadgeVariants = VariantProps<typeof badgeVariants>
