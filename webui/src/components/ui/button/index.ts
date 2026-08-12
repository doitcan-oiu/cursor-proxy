import type { VariantProps } from 'class-variance-authority'
import { cva } from 'class-variance-authority'

export { default as Button } from './Button.vue'

/*
 * 按钮体系遵循 DESIGN.md：所有按钮一律药丸形（rounded-full），
 * 两级主次——黑色实心 primary 与描边 secondary，白底 tertiary 作第三层。
 */
export const buttonVariants = cva(
  [
    'inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-full',
    'text-sm font-semibold leading-[1.4] transition-all duration-150 select-none',
    'disabled:pointer-events-none disabled:opacity-60',
    'outline-none focus-visible:ring-3 focus-visible:ring-ring/40',
    "[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
  ],
  {
    variants: {
      variant: {
        // 黑色药丸：全站主 CTA
        default: 'bg-ink text-white hover:bg-charcoal active:bg-charcoal',
        // 描边药丸：与主按钮成对出现的次级操作
        secondary: 'bg-transparent text-ink border border-ink hover:bg-ink/5',
        // 白底细线药丸：信息性/第三层操作
        tertiary: 'bg-canvas text-ink border border-hairline hover:bg-surface',
        // 无边框：表格行内、工具条
        ghost: 'bg-transparent text-steel hover:bg-surface hover:text-ink',
        // 危险操作
        destructive: 'bg-danger text-white hover:bg-danger/90',
        destructiveGhost: 'bg-transparent text-steel hover:bg-danger-bg hover:text-danger',
        // 文本链接
        link: 'text-ink font-medium underline-offset-4 hover:underline px-0',
      },
      size: {
        default: 'h-10 px-6',
        sm: 'h-8 px-4 text-[13px]',
        xs: 'h-7 px-3 text-[12px] gap-1.5',
        lg: 'h-12 px-8 text-[15px]',
        icon: 'size-9',
        iconSm: 'size-8',
        iconXs: 'size-7',
      },
    },
    defaultVariants: {
      variant: 'default',
      size: 'default',
    },
  },
)

export type ButtonVariants = VariantProps<typeof buttonVariants>
