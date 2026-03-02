import { Routes } from '@angular/router';
import { authGuard } from './guards/auth.guard';
import { adminGuard } from './guards/admin.guard';
import { LayoutComponent } from './layout/layout.component';

export const routes: Routes = [
  { path: 'login', loadComponent: () => import('./pages/login/login.component').then(m => m.LoginComponent) },
  { path: 'callback', loadComponent: () => import('./pages/login/login.component').then(m => m.LoginComponent) },
  {
    path: '',
    component: LayoutComponent,
    canActivate: [authGuard],
    children: [
      { path: '', redirectTo: 'dashboard', pathMatch: 'full' },
      { path: 'dashboard', loadComponent: () => import('./pages/dashboard/dashboard.component').then(m => m.DashboardComponent) },
      { path: 'websites', loadComponent: () => import('./pages/websites/websites.component').then(m => m.WebsitesComponent) },
      { path: 'databases', loadComponent: () => import('./pages/databases/databases.component').then(m => m.DatabasesComponent) },
      { path: 'emails', loadComponent: () => import('./pages/emails/emails.component').then(m => m.EmailsComponent) },
      { path: 'dns', loadComponent: () => import('./pages/dns/dns.component').then(m => m.DnsComponent) },
      { path: 'certificates', loadComponent: () => import('./pages/certificates/certificates.component').then(m => m.CertificatesComponent) },
      { path: 'backups', loadComponent: () => import('./pages/backups/backups.component').then(m => m.BackupsComponent) },
      { path: 'users', loadComponent: () => import('./pages/users/users.component').then(m => m.UsersComponent), canActivate: [adminGuard] },
      { path: 'hosting-plans', loadComponent: () => import('./pages/hosting-plans/hosting-plans.component').then(m => m.HostingPlansComponent), canActivate: [adminGuard] },
    ]
  },
  { path: '**', redirectTo: '' }
];
