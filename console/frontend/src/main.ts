// Design tokens first: every component reads its colours, type ramp and radii
// from these custom properties, so they must be defined before any component
// stylesheet is applied.
import './styles/tokens.css'

import { mount } from 'svelte'
import App from './App.svelte'

const app = mount(App, {
  target: document.getElementById('app')!,
})

export default app
