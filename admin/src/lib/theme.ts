import type { ThemeConfig } from 'antd'

/** Design tokens for the whole panel (same palette as the app). Change colors / radii here only. */
export const colors = {
  brand: '#15171c', // ink: primary actions
  violet: '#b0662c', // copper accent
  success: '#12a150',
  warning: '#d9822b',
  danger: '#d64545',
  sky: '#3d6fb6',
  sider: '#15171c',
  bg: '#f5f4f0',
  muted: '#8a8a8e',
}

export const theme: ThemeConfig = {
  token: {
    colorPrimary: colors.brand,
    colorLink: '#2f5fa8',
    colorSuccess: colors.success,
    colorWarning: colors.warning,
    colorError: colors.danger,
    colorBgLayout: colors.bg,
    colorBorderSecondary: '#e8e6e0',
    borderRadius: 8,
    borderRadiusLG: 12,
    fontFamily: `-apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', sans-serif`,
  },
  components: {
    Layout: { siderBg: colors.sider, headerBg: '#fff', headerPadding: '0 20px', headerHeight: 60 },
    Menu: {
      darkItemBg: colors.sider,
      darkItemSelectedBg: '#2b2f38',
      darkItemHoverBg: '#22252c',
      itemBorderRadius: 8,
      itemMarginInline: 8,
      itemHeight: 44,
    },
    Card: { headerFontSize: 15 },
    Table: { headerBg: '#faf9f6', headerColor: '#5f6066' },
    Segmented: { itemSelectedBg: '#fff', trackBg: '#efede7' },
  },
}
