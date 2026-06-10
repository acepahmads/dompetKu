/** @type {import('tailwindcss').Config} */
export default {
  content: [
    "./index.html",
    "./src/**/*.{vue,js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        'wa-green': '#25D366',
        'wa-dark-green': '#075E54',
        'wa-chat-bg': '#0b141a',
        'wa-bubble-sent': '#005c4b',
        'wa-bubble-received': '#202c33',
        'bg-app': '#090d16',
        'bg-surface': '#121826',
        'bg-surface-elevated': '#1e2640',
        'border-custom': 'rgba(255, 255, 255, 0.08)',
        'text-primary': '#f8fafc',
        'text-secondary': '#94a3b8',
        'text-muted': '#64748b',
        'income': '#10b981',
        'expense': '#f43f5e',
        'planned': '#f59e0b',
        'info-blue': '#3b82f6',
      },
      fontFamily: {
        primary: ['"Plus Jakarta Sans"', 'sans-serif'],
        playpen: ['"Playpen Sans"', 'sans-serif'],
      }
    },
  },
  plugins: [],
}
