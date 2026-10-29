import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AuthProvider } from './lib/auth'
import { ToastProvider } from './lib/toast'
import { I18nProvider } from './lib/i18n'
import { ProtectedRoute } from './components/ProtectedRoute'
import { LoginPage } from './pages/LoginPage'
import { DashboardLayout } from './pages/DashboardLayout'
import { DashboardPage } from './pages/DashboardPage'
import { NodesPage } from './pages/NodesPage'
import { AccountsPage } from './pages/AccountsPage'
import { ApiKeysPage } from './pages/ApiKeysPage'
import { AdminUsersPage } from './pages/AdminUsersPage'
import { AuditLogPage } from './pages/AuditLogPage'
import { SettingsPage } from './pages/SettingsPage'
import { ResellersPage } from './pages/ResellersPage'
import { ReportsPage } from './pages/ReportsPage'
import { WebhooksPage } from './pages/WebhooksPage'
import { NotificationsPage } from './pages/NotificationsPage'

const queryClient = new QueryClient()

function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <I18nProvider>
        <BrowserRouter>
          <AuthProvider>
            <ToastProvider>
              <Routes>
                <Route path="/login" element={<LoginPage />} />
                <Route
                  element={
                    <ProtectedRoute>
                      <DashboardLayout />
                    </ProtectedRoute>
                  }
                >
                  <Route path="/dashboard" element={<DashboardPage />} />
                  <Route path="/nodes" element={<NodesPage />} />
                  <Route path="/accounts" element={<AccountsPage />} />
                  <Route path="/api-keys" element={<ApiKeysPage />} />
                  <Route path="/admins" element={<AdminUsersPage />} />
                  <Route path="/audit-log" element={<AuditLogPage />} />
                  <Route path="/settings" element={<SettingsPage />} />
                  <Route path="/resellers" element={<ResellersPage />} />
                  <Route path="/reports" element={<ReportsPage />} />
                  <Route path="/webhooks" element={<WebhooksPage />} />
                  <Route path="/notifications" element={<NotificationsPage />} />
                </Route>
                <Route path="*" element={<Navigate to="/dashboard" replace />} />
              </Routes>
            </ToastProvider>
          </AuthProvider>
        </BrowserRouter>
      </I18nProvider>
    </QueryClientProvider>
  )
}

export default App
