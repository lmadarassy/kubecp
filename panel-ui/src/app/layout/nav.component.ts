import { Component } from '@angular/core';
import { RouterLink, RouterLinkActive } from '@angular/router';
import { NgIf } from '@angular/common';
import { ApiService } from '../services/api.service';
import { AuthService } from '../services/auth.service';

@Component({
  selector: 'app-nav',
  standalone: true,
  imports: [RouterLink, RouterLinkActive, NgIf],
  template: `
    <nav class="sidebar">
      <div class="sidebar-header">
        <h3>Hosting Panel</h3>
      </div>
      <div *ngIf="auth.impersonatedUser" class="impersonation-banner">
        <span>👤 {{ auth.impersonatedUser }}</span>
      </div>
      <ul class="nav-list">
        <li><a routerLink="/dashboard" routerLinkActive="active">Dashboard</a></li>
        <li><a routerLink="/websites" routerLinkActive="active">Websites</a></li>
        <li><a routerLink="/databases" routerLinkActive="active">Databases</a></li>
        <li><a routerLink="/emails" routerLinkActive="active">Email</a></li>
        <li><a routerLink="/dns" routerLinkActive="active">DNS</a></li>
        <li><a routerLink="/certificates" routerLinkActive="active">Certificates</a></li>
        <li><a routerLink="/backups" routerLinkActive="active">Backups</a></li>
        <li *ngIf="auth.isAdmin" class="nav-divider"></li>
        <li *ngIf="auth.isAdmin"><a routerLink="/users" routerLinkActive="active">Users</a></li>
        <li *ngIf="auth.isAdmin"><a routerLink="/hosting-plans" routerLinkActive="active">Hosting Plans</a></li>
      </ul>
      <div class="sidebar-footer">
        <span class="user-info">{{ auth.username }}</span>
        <span *ngIf="auth.isAdmin && !auth.impersonatedUser" class="badge blue">admin</span>
        <button *ngIf="auth.impersonatedUser" class="exit-btn" (click)="exitImpersonation()">Exit Impersonation</button>
        <button (click)="auth.logout()">Logout</button>
      </div>
    </nav>
  `,
  styles: [`
    .sidebar {
      width: 220px; height: 100vh; background: #1e293b; color: #e2e8f0;
      display: flex; flex-direction: column; position: fixed; left: 0; top: 0;
    }
    .sidebar-header { padding: 20px 16px; border-bottom: 1px solid #334155; }
    .sidebar-header h3 { margin: 0; font-size: 16px; color: #fff; }
    .impersonation-banner {
      background: #92400e; color: #fef3c7; padding: 8px 16px;
      font-size: 12px; font-weight: 600; text-align: center;
    }
    .nav-list { list-style: none; padding: 8px 0; margin: 0; flex: 1; overflow-y: auto; }
    .nav-list li a {
      display: block; padding: 10px 16px; color: #cbd5e1; text-decoration: none;
      transition: background 0.15s;
    }
    .nav-list li a:hover { background: #334155; color: #fff; }
    .nav-list li a.active { background: #2563eb; color: #fff; }
    .nav-divider { border-top: 1px solid #334155; margin: 8px 0; }
    .sidebar-footer {
      padding: 12px 16px; border-top: 1px solid #334155;
      display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
    }
    .user-info { font-size: 13px; }
    .sidebar-footer button {
      margin-top: 4px; width: 100%; padding: 6px; background: #475569;
      color: #e2e8f0; border: none; border-radius: 4px; cursor: pointer; font-size: 13px;
    }
    .sidebar-footer button:hover { background: #64748b; }
    .exit-btn { background: #92400e !important; }
    .exit-btn:hover { background: #b45309 !important; }
    .badge { font-size: 11px; }
  `]
})
export class NavComponent {
  constructor(public auth: AuthService, private api: ApiService) {}

  exitImpersonation(): void {
    this.api.exitImpersonation().subscribe({
      next: () => this.auth.setImpersonatedUser(null),
      error: () => this.auth.setImpersonatedUser(null)
    });
  }
}
