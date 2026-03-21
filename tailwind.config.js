/** @type {import('tailwindcss').Config} */
module.exports = {
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
