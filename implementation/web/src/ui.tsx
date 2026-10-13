// Shared interaction primitives, built on Radix.
//
// The architecture plan already called for Radix base components; the dialogs
// and menus had been hand-rolled instead, which meant focus trapping, Escape
// handling, roving focus inside menus, and screen-reader semantics were all
// missing. Radix supplies those behaviours and no visual opinion, so the look
// stays defined by the stylesheet in this repository.
//
// Everything exported here is styled through the classes in ui.css, so there is
// exactly one place to change how a dialog or a menu looks.
import {
  createContext, useContext, useMemo, useState, type ReactNode,
} from 'react'
import * as Dialog from '@radix-ui/react-dialog'
import * as DropdownMenu from '@radix-ui/react-dropdown-menu'
import * as Tooltip from '@radix-ui/react-tooltip'
import * as SelectPrimitive from '@radix-ui/react-select'
import * as CheckboxPrimitive from '@radix-ui/react-checkbox'
import * as CollapsiblePrimitive from '@radix-ui/react-collapsible'
import * as RadioGroupPrimitive from '@radix-ui/react-radio-group'
import { Check, ChevronDown, Search, X } from 'lucide-react'
import './ui.css'

// --- dialog -----------------------------------------------------------------

export function Modal({ open, onOpenChange, title, description, children, footer }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description?: string
  children?: ReactNode
  footer?: ReactNode
}) {
  return <Dialog.Root open={open} onOpenChange={onOpenChange}>
    <Dialog.Portal>
      <Dialog.Overlay className="ui-overlay" />
      <Dialog.Content className="ui-dialog" aria-describedby={description ? undefined : undefined}>
        <div className="ui-dialog-head">
          <Dialog.Title className="ui-dialog-title">{title}</Dialog.Title>
          {description && <Dialog.Description className="ui-dialog-description">{description}</Dialog.Description>}
        </div>
        <div className="ui-dialog-body">{children}</div>
        {footer && <div className="ui-dialog-footer">{footer}</div>}
        <Dialog.Close className="ui-dialog-close" aria-label="关闭"><X size={16} /></Dialog.Close>
      </Dialog.Content>
    </Dialog.Portal>
  </Dialog.Root>
}

// ConfirmDialog is the one confirmation shape in the product: a title, the
// consequence in plain words, and a destructive action named by what it does.
export function ConfirmDialog({ open, onOpenChange, title, description, confirmLabel, onConfirm, busy, tone = 'danger' }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: string
  confirmLabel: string
  onConfirm: () => void
  busy?: boolean
  tone?: 'danger' | 'default'
}) {
  return <Modal open={open} onOpenChange={onOpenChange} title={title} description={description}
    footer={<>
      <button type="button" className="button button-secondary" onClick={() => onOpenChange(false)} disabled={busy}>取消</button>
      <button type="button" className={tone === 'danger' ? 'button button-danger' : 'button button-primary'} onClick={onConfirm} disabled={busy}>{busy ? '正在处理…' : confirmLabel}</button>
    </>} />
}

// --- dropdown menu ----------------------------------------------------------
//
// A menu is a list of actions. Radix moves focus with the arrow keys, closes on
// Escape, and restores focus to the trigger, none of which the previous
// hand-written version did.

export function Menu({ trigger, children, align = 'end', label }: {
  trigger: ReactNode
  children: ReactNode
  align?: 'start' | 'center' | 'end'
  label: string
}) {
  return <DropdownMenu.Root>
    <DropdownMenu.Trigger className="ui-menu-trigger" aria-label={label}>{trigger}</DropdownMenu.Trigger>
    <DropdownMenu.Portal>
      <DropdownMenu.Content className="ui-menu" align={align} sideOffset={6}>
        {children}
      </DropdownMenu.Content>
    </DropdownMenu.Portal>
  </DropdownMenu.Root>
}

export function MenuLabel({ children }: { children: ReactNode }) {
  return <DropdownMenu.Label className="ui-menu-label">{children}</DropdownMenu.Label>
}

export function MenuItem({ children, onSelect, asChild, disabled }: {
  children: ReactNode
  onSelect?: () => void
  asChild?: boolean
  disabled?: boolean
}) {
  return <DropdownMenu.Item className="ui-menu-item" onSelect={onSelect} asChild={asChild} disabled={disabled}>{children}</DropdownMenu.Item>
}

export function MenuSeparator() {
  return <DropdownMenu.Separator className="ui-menu-separator" />
}

// --- multi select -----------------------------------------------------------
//
// A dropdown of checkboxes rather than a row of toggle chips. The options are a
// repository's labels and members, which can run to dozens, and a chip row would
// reflow the page every time the selection changes.
//
// The menu deliberately stays open while boxes are ticked, so choosing three
// labels is one visit rather than three. The summary shows what is selected, so
// the control still reads correctly once the menu is closed.
export function MultiPicker({ options, selected, onChange, ariaLabel, placeholder = '全部' }: {
  options: Option[]
  selected: string[]
  onChange: (values: string[]) => void
  ariaLabel: string
  placeholder?: string
}) {
  const chosen = options.filter(option => selected.includes(option.value))
  // A selection whose option has since disappeared is still reported, otherwise
  // the count would silently disagree with what is being filtered on.
  const orphaned = selected.length - chosen.length
  return <DropdownMenu.Root>
    <DropdownMenu.Trigger className="ui-multipicker-trigger" aria-label={ariaLabel}>
      <span className={`ui-multipicker-value${selected.length === 0 ? ' empty' : ''}`}>{selected.length === 0 ? placeholder : chosen.map(option => option.label).join('、') || `已选 ${selected.length} 项`}</span>
      {orphaned > 0 && <span className="ui-multipicker-count">{selected.length}</span>}
      <ChevronDown size={14} />
    </DropdownMenu.Trigger>
    <DropdownMenu.Portal>
      <DropdownMenu.Content className="ui-menu ui-multipicker-menu" align="start" sideOffset={6}>
        {options.length === 0 && <DropdownMenu.Item className="ui-menu-item" disabled>没有可选项</DropdownMenu.Item>}
        {options.map(option => {
          const checked = selected.includes(option.value)
          return <DropdownMenu.CheckboxItem key={option.value} className="ui-menu-item ui-multipicker-item" checked={checked}
            onCheckedChange={next => onChange(next ? [...selected, option.value] : selected.filter(value => value !== option.value))}
            onSelect={event => event.preventDefault()}>
            <span className="ui-multipicker-box">{checked && <Check size={12} />}</span>
            <span className="ui-multipicker-item-text">{option.label}</span>
          </DropdownMenu.CheckboxItem>
        })}
        {selected.length > 0 && <><DropdownMenu.Separator className="ui-menu-separator" /><DropdownMenu.Item className="ui-menu-item" onSelect={() => onChange([])}>清除选择</DropdownMenu.Item></>}
      </DropdownMenu.Content>
    </DropdownMenu.Portal>
  </DropdownMenu.Root>
}

// --- tooltip ----------------------------------------------------------------
//
// Replaces title="", which the browser renders slowly, cannot be styled, and
// never appears for keyboard users.

export function Hint({ label, children, side = 'top' }: { label: string; children: ReactNode; side?: 'top' | 'bottom' | 'left' | 'right' }) {
  return <Tooltip.Root>
    <Tooltip.Trigger asChild>{children}</Tooltip.Trigger>
    <Tooltip.Portal>
      <Tooltip.Content className="ui-tooltip" side={side} sideOffset={6}>{label}<Tooltip.Arrow className="ui-tooltip-arrow" /></Tooltip.Content>
    </Tooltip.Portal>
  </Tooltip.Root>
}

export function TooltipProvider({ children }: { children: ReactNode }) {
  return <Tooltip.Provider delayDuration={400} skipDelayDuration={200}>{children}</Tooltip.Provider>
}

// --- select -----------------------------------------------------------------
//
// A styled listbox with a type-ahead search. The previous native <select> could
// not be searched, which made the branch picker unusable once a repository had
// more than a handful of branches.

export interface Option { value: string; label: string; hint?: string }

export function Picker({ value, onChange, options, placeholder = '请选择', ariaLabel, disabled, searchable, className, icon }: {
  value: string
  onChange: (value: string) => void
  options: Option[]
  placeholder?: string
  ariaLabel: string
  disabled?: boolean
  // Enabled automatically past eight options, where scanning a list becomes
  // slower than typing.
  searchable?: boolean
  className?: string
  // Shown inside the trigger, before the selected value. A control like the
  // branch picker reads as one object when its icon is part of the button
  // rather than floating beside it.
  icon?: ReactNode
}) {
  const [query, setQuery] = useState('')
  const withSearch = searchable ?? options.length > 8
  const filtered = useMemo(() => {
    if (!query.trim()) return options
    const needle = query.trim().toLowerCase()
    return options.filter(option => option.label.toLowerCase().includes(needle) || option.value.toLowerCase().includes(needle))
  }, [options, query])
  const selected = options.find(option => option.value === value)
  return <SelectPrimitive.Root value={value || undefined} onValueChange={onChange} disabled={disabled}>
    <SelectPrimitive.Trigger className={`ui-select-trigger${className ? ' ' + className : ''}`} aria-label={ariaLabel}>
      {icon}
      <SelectPrimitive.Value placeholder={placeholder}>{selected?.label ?? placeholder}</SelectPrimitive.Value>
      <SelectPrimitive.Icon><ChevronDown size={14} /></SelectPrimitive.Icon>
    </SelectPrimitive.Trigger>
    <SelectPrimitive.Portal>
      <SelectPrimitive.Content className="ui-select-content" position="popper" sideOffset={4}>
        {withSearch && <div className="ui-select-search">
          <Search size={13} />
          <input value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索…" aria-label="搜索选项" onKeyDown={event => event.stopPropagation()} />
        </div>}
        <SelectPrimitive.Viewport className="ui-select-viewport">
          {filtered.length === 0 && <div className="ui-select-empty">没有匹配项</div>}
          {filtered.map(option => <SelectPrimitive.Item key={option.value} value={option.value} className="ui-select-item">
            <SelectPrimitive.ItemIndicator className="ui-select-indicator"><Check size={13} /></SelectPrimitive.ItemIndicator>
            <SelectPrimitive.ItemText>{option.label}</SelectPrimitive.ItemText>
            {option.hint && <span className="ui-select-hint">{option.hint}</span>}
          </SelectPrimitive.Item>)}
        </SelectPrimitive.Viewport>
      </SelectPrimitive.Content>
    </SelectPrimitive.Portal>
  </SelectPrimitive.Root>
}

// --- collapsible ------------------------------------------------------------
//
// One disclosure component, so the trigger is a real <button> with
// aria-expanded and aria-controls wired to the region it controls. The previous
// hand-written panel managed its own boolean and left a bare text button.

export function Disclosure({ title, description, icon, children, defaultOpen = false }: {
  title: string
  description?: string
  icon?: ReactNode
  children: ReactNode
  defaultOpen?: boolean
}) {
  const [open, setOpen] = useState(defaultOpen)
  return <CollapsiblePrimitive.Root className="ui-disclosure" open={open} onOpenChange={setOpen}>
    <CollapsiblePrimitive.Trigger className="ui-disclosure-trigger">
      {icon && <span className="ui-disclosure-icon">{icon}</span>}
      <span className="ui-disclosure-text"><strong>{title}</strong>{description && <small>{description}</small>}</span>
      <ChevronDown className={open ? 'rotated' : ''} size={16} />
    </CollapsiblePrimitive.Trigger>
    <CollapsiblePrimitive.Content className="ui-disclosure-content">{children}</CollapsiblePrimitive.Content>
  </CollapsiblePrimitive.Root>
}

// --- checkbox and radio -----------------------------------------------------
//
// These were the last hand-written controls, and they carried a real defect:
// the native input was hidden behind opacity:0 plus pointer-events:none and a
// decorative shape pretended to be the control, so tabbing to it showed no
// focus ring. Radix keeps the real input in the accessibility tree and gives it
// a visible focus state and an indeterminate attribute a screen reader reports.

export function CheckboxControl({ checked, onCheckedChange, disabled, id }: {
  checked: boolean
  onCheckedChange: (checked: boolean) => void
  disabled?: boolean
  id?: string
}) {
  return <CheckboxPrimitive.Root id={id} className="ui-check-box" checked={checked} onCheckedChange={value => onCheckedChange(value === true)} disabled={disabled}>
    <CheckboxPrimitive.Indicator className="ui-check-tick"><Check size={13} strokeWidth={3} /></CheckboxPrimitive.Indicator>
  </CheckboxPrimitive.Root>
}

export function Checkbox({ checked, onCheckedChange, disabled, label, description, id }: {
  checked: boolean
  onCheckedChange: (checked: boolean) => void
  disabled?: boolean
  label: ReactNode
  description?: ReactNode
  id?: string
}) {
  return <label className={`ui-check${disabled ? ' ui-check-disabled' : ''}`} htmlFor={id}>
    <CheckboxControl id={id} checked={checked} onCheckedChange={onCheckedChange} disabled={disabled} />
    <span className="ui-check-text"><strong>{label}</strong>{description && <small>{description}</small>}</span>
  </label>
}

export function RadioGroup({ value, onValueChange, options, ariaLabel, name }: {
  value: string
  onValueChange: (value: string) => void
  options: Array<{ value: string; label: ReactNode; description?: ReactNode; icon?: ReactNode }>
  ariaLabel: string
  name?: string
}) {
  return <RadioGroupPrimitive.Root className="ui-radio-group" value={value} onValueChange={onValueChange} aria-label={ariaLabel} name={name}>
    {options.map(option => <RadioGroupPrimitive.Item key={option.value} value={option.value} className="ui-radio" id={`${name || 'radio'}-${option.value}`}>
      <span className="ui-radio-dot" />
      {option.icon && <span className="ui-radio-icon">{option.icon}</span>}
      <span className="ui-radio-text"><strong>{option.label}</strong>{option.description && <small>{option.description}</small>}</span>
    </RadioGroupPrimitive.Item>)}
  </RadioGroupPrimitive.Root>
}

// --- segmented control ------------------------------------------------------

export function Segmented<T extends string>({ value, onChange, options, ariaLabel }: {
  value: T
  onChange: (value: T) => void
  options: Array<{ value: T; label: string }>
  ariaLabel: string
}) {
  return <div className="ui-segmented" role="group" aria-label={ariaLabel}>
    {options.map(option => <button key={option.value} type="button" className={option.value === value ? 'active' : ''} aria-pressed={option.value === value} onClick={() => onChange(option.value)}>{option.label}</button>)}
  </div>
}

// --- pagination -------------------------------------------------------------
//
// Five pages each hand-rolled the same previous/next pair. One component means
// the boundary rules (page one disables "previous", the last page disables
// "next") are decided once.

export function Pagination({ page, hasMore, hasPrevious, onPage, label }: {
  page: number
  hasMore: boolean
  hasPrevious?: boolean
  onPage: (page: number) => void
  label: string
}) {
  const canGoBack = hasPrevious ?? page > 1
  if (!canGoBack && !hasMore) return null
  return <nav className="ui-pagination" aria-label={label}>
    <button type="button" className="button button-secondary button-small" disabled={!canGoBack} onClick={() => onPage(page - 1)}>上一页</button>
    <span className="ui-pagination-page">第 {page} 页</span>
    <button type="button" className="button button-secondary button-small" disabled={!hasMore} onClick={() => onPage(page + 1)}>下一页</button>
  </nav>
}

// --- clipboard --------------------------------------------------------------

const CopyContext = createContext<{ copied: string | null; copy: (value: string, key?: string) => void }>({ copied: null, copy: () => {} })

export function CopyProvider({ children }: { children: ReactNode }) {
  const [copied, setCopied] = useState<string | null>(null)
  const value = useMemo(() => ({
    copied,
    copy: (text: string, key?: string) => {
      void navigator.clipboard?.writeText(text).then(() => {
        setCopied(key ?? text)
        window.setTimeout(() => setCopied(current => (current === (key ?? text) ? null : current)), 2000)
      })
    },
  }), [copied])
  return <CopyContext.Provider value={value}>{children}</CopyContext.Provider>
}

export function useCopy() {
  return useContext(CopyContext)
}
