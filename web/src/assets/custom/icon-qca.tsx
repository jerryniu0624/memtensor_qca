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
import { useId, type SVGProps } from 'react'

type IconQcaProps = SVGProps<SVGSVGElement> & {
  size?: number
}

// Qoder Cloud Agent has no bundled brand asset, so the channel uses this mark.
export function IconQca({ size = 20, ...props }: IconQcaProps) {
  const gradientId = useId()

  return (
    <svg
      xmlns='http://www.w3.org/2000/svg'
      viewBox='0 0 24 24'
      width={size}
      height={size}
      {...props}
    >
      <defs>
        <linearGradient
          id={gradientId}
          x1='4'
          y1='4'
          x2='20'
          y2='20'
          gradientUnits='userSpaceOnUse'
        >
          <stop stopColor='#7C9CFF' />
          <stop offset='.52' stopColor='#5B7CFA' />
          <stop offset='1' stopColor='#9B5CF6' />
        </linearGradient>
      </defs>
      <g
        fill='none'
        stroke={`url(#${gradientId})`}
        strokeLinecap='round'
        strokeLinejoin='round'
        strokeWidth='1.8'
      >
        <path d='M17.5 19H9a7 7 0 1 1 6.71-9h1.79a4.5 4.5 0 1 1 0 9Z' />
        <path
          d='M12 11.2l.8 1.9 1.9.8-1.9.8-.8 1.9-.8-1.9-1.9-.8 1.9-.8Z'
          strokeWidth='1.4'
        />
      </g>
    </svg>
  )
}
