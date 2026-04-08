/** @type {import('tailwindcss').Config} */
module.exports = {
  darkMode: 'class',
  content: ["./router/view/templates/**/*.html"],
  theme: {
    extend: {
      fontFamily: {
        sans: ['"Noto Sans TC"', 'system-ui', 'sans-serif'],
      },
    },
  },
  plugins: [],
}
