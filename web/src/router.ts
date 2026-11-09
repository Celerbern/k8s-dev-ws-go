import { createRouter, createWebHistory } from 'vue-router'
import { auth } from './store'

const LoginView = () => import('./views/LoginView.vue')
const DashboardView = () => import('./views/DashboardView.vue')
const WorkspaceView = () => import('./views/WorkspaceView.vue')
const ProfileView = () => import('./views/ProfileView.vue')
const AdminView = () => import('./views/AdminView.vue')

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', component: LoginView },
    { path: '/', component: DashboardView },
    { path: '/workspace/:name', component: WorkspaceView },
    { path: '/profile', component: ProfileView },
    { path: '/admin', component: AdminView },
  ],
})

router.beforeEach(async (to) => {
  if (!auth.loaded) await auth.load()
  if (to.path !== '/login' && !auth.user) return '/login'
  if (to.path === '/login' && auth.user) return '/'
  if (to.path === '/admin' && auth.user?.role !== 'admin') return '/'
})