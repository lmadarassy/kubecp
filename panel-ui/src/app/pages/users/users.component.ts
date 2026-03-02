import { Component, OnInit } from '@angular/core';
import { NgIf, NgFor } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from '../../services/api.service';
import { AuthService } from '../../services/auth.service';
import { User, HostingPlan } from '../../models';

@Component({
  selector: 'app-users',
  standalone: true,
  imports: [NgIf, NgFor, FormsModule],
  template: `
    <div class="page-header">
      <h2>Users</h2>
      <button class="primary" (click)="toggleCreate()">{{ showForm ? 'Cancel' : 'Create User' }}</button>
    </div>

    <div *ngIf="error" class="alert error">{{ error }}</div>
    <div *ngIf="success" class="alert success">{{ success }}</div>

    <!-- Create / Edit form -->
    <div *ngIf="showForm" class="card">
      <h3>{{ editingUser ? 'Edit User' : 'Create User' }}</h3>
      <div class="form-group">
        <label>Username</label>
        <input [(ngModel)]="form.username" [disabled]="!!editingUser" placeholder="username">
      </div>
      <div class="form-group">
        <label>Email</label>
        <input [(ngModel)]="form.email" type="email" placeholder="user@example.com">
      </div>
      <div class="form-group">
        <label>First Name</label>
        <input [(ngModel)]="form.firstName" placeholder="First name">
      </div>
      <div class="form-group">
        <label>Last Name</label>
        <input [(ngModel)]="form.lastName" placeholder="Last name">
      </div>
      <div *ngIf="!editingUser" class="form-group">
        <label>Password</label>
        <input [(ngModel)]="form.password" type="password" placeholder="Password">
      </div>
      <div class="form-group">
        <label>Hosting Plan</label>
        <select [(ngModel)]="form.hostingPlanId">
          <option value="">— None —</option>
          <option *ngFor="let p of plans" [value]="p.name">{{ p.displayName }}</option>
        </select>
      </div>
      <button class="primary" (click)="save()">{{ editingUser ? 'Update' : 'Create' }}</button>
    </div>

    <!-- Password change -->
    <div *ngIf="changingPasswordFor" class="card">
      <h3>Change Password — {{ changingPasswordFor.username }}</h3>
      <div class="form-group">
        <label>New Password</label>
        <input [(ngModel)]="newPassword" type="password" placeholder="New password">
      </div>
      <button class="primary" (click)="changePassword()">Change Password</button>
      <button (click)="changingPasswordFor = null">Cancel</button>
    </div>

    <!-- User list -->
    <table>
      <thead>
        <tr><th>Username</th><th>Email</th><th>Name</th><th>Role</th><th>Hosting Plan</th><th>Actions</th></tr>
      </thead>
      <tbody>
        <tr *ngFor="let u of users">
          <td>{{ u.username }}</td>
          <td>{{ u.email }}</td>
          <td>{{ u.firstName }} {{ u.lastName }}</td>
          <td><span class="badge" [class.blue]="u.role === 'admin'" [class.green]="u.role === 'user'">{{ u.role }}</span></td>
          <td>{{ planName(u.hostingPlanId) }}</td>
          <td>
            <button (click)="edit(u)">Edit</button>
            <button (click)="changingPasswordFor = u; newPassword = ''">Password</button>
            <button (click)="impersonate(u)">Impersonate</button>
            <button class="danger" (click)="remove(u)">Delete</button>
          </td>
        </tr>
      </tbody>
    </table>

    <!-- Impersonation banner -->
    <div *ngIf="impersonating" class="alert" style="background:#fef3c7;border:1px solid #f59e0b;color:#92400e;margin-top:16px">
      Viewing as: <strong>{{ impersonating }}</strong>
      <button (click)="exitImpersonation()" style="margin-left:16px">Exit Impersonation</button>
    </div>
  `
})
export class UsersComponent implements OnInit {
  users: User[] = [];
  plans: HostingPlan[] = [];
  showForm = false;
  editingUser: User | null = null;
  changingPasswordFor: User | null = null;
  newPassword = '';
  form = { username: '', email: '', firstName: '', lastName: '', password: '', hostingPlanId: '' };
  impersonating: string | null = null;
  error = '';
  success = '';

  constructor(private api: ApiService, private auth: AuthService) {}

  ngOnInit(): void {
    this.impersonating = this.auth.impersonatedUser;
    this.load();
    this.api.getHostingPlans().subscribe({ next: p => this.plans = p });
  }

  load(): void {
    this.api.getUsers().subscribe({
      next: u => this.users = u,
      error: () => this.error = 'Failed to load users.'
    });
  }

  planName(id?: string): string {
    if (!id) return '—';
    return this.plans.find(p => p.name === id || p.id === id)?.displayName || id;
  }

  edit(u: User): void {
    this.editingUser = u;
    this.form = { username: u.username, email: u.email, firstName: u.firstName, lastName: u.lastName, password: '', hostingPlanId: u.hostingPlanId || '' };
    this.showForm = true;
  }

  toggleCreate(): void {
    if (this.showForm && !this.editingUser) {
      this.showForm = false;
    } else {
      this.reset();
      this.showForm = true;
    }
  }

  save(): void {
    this.error = ''; this.success = '';
    if (this.editingUser) {
      this.api.updateUser(this.editingUser.id, this.form).subscribe({
        next: () => { this.success = 'User updated.'; this.reset(); this.load(); },
        error: (e) => this.error = e.error?.error?.message || 'Update failed.'
      });
    } else {
      this.api.createUser(this.form as Partial<User> & { password: string }).subscribe({
        next: () => { this.success = 'User created.'; this.reset(); this.load(); },
        error: (e) => this.error = e.error?.error?.message || 'Creation failed.'
      });
    }
  }

  remove(u: User): void {
    if (!confirm(`Delete user "${u.username}"?`)) return;
    this.api.deleteUser(u.id).subscribe({
      next: () => { this.success = 'User deleted.'; this.load(); },
      error: (e) => this.error = e.error?.error?.message || 'Delete failed.'
    });
  }

  changePassword(): void {
    if (!this.changingPasswordFor || !this.newPassword) return;
    this.api.changePassword(this.changingPasswordFor.id, this.newPassword).subscribe({
      next: () => { this.success = 'Password changed.'; this.changingPasswordFor = null; },
      error: (e) => this.error = e.error?.error?.message || 'Password change failed.'
    });
  }

  reset(): void {
    this.showForm = false;
    this.editingUser = null;
    this.form = { username: '', email: '', firstName: '', lastName: '', password: '', hostingPlanId: '' };
  }

  impersonate(u: User): void {
    this.api.impersonateUser(u.id).subscribe({
      next: (resp: any) => {
        this.impersonating = resp.impersonating || u.username;
        this.auth.setImpersonatedUser(this.impersonating);
        this.success = `Now viewing as ${this.impersonating}`;
      },
      error: (e: any) => this.error = e.error?.error?.message || 'Impersonation failed.'
    });
  }

  exitImpersonation(): void {
    this.api.exitImpersonation().subscribe({
      next: () => {
        this.impersonating = null;
        this.auth.setImpersonatedUser(null);
        this.success = 'Returned to admin view.';
      },
      error: () => {
        this.impersonating = null;
        this.auth.setImpersonatedUser(null);
      }
    });
  }
}
