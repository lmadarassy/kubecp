import { Component, OnInit } from '@angular/core';
import { NgIf, NgFor, NgClass } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from '../../services/api.service';
import { Database } from '../../models';

@Component({
  selector: 'app-databases',
  standalone: true,
  imports: [NgIf, NgFor, NgClass, FormsModule],
  template: `
    <div class="page-header">
      <h2>Databases</h2>
      <div>
        <a [href]="pmaUrl" target="_blank" style="display:inline-block;padding:6px 16px;background:#6366f1;color:#fff;border-radius:6px;text-decoration:none;margin-right:8px">phpMyAdmin</a>
        <button class="primary" (click)="showCreate = !showCreate">{{ showCreate ? 'Cancel' : 'Create Database' }}</button>
      </div>
    </div>

    <div *ngIf="error" class="alert error">{{ error }}</div>
    <div *ngIf="success" class="alert success">{{ success }}</div>

    <div *ngIf="showCreate" class="card">
      <h3>Create Database</h3>
      <div class="form-group">
        <label>Name</label>
        <input [(ngModel)]="form.name" placeholder="my_database">
      </div>
      <div class="form-group">
        <label>Charset</label>
        <select [(ngModel)]="form.charset">
          <option value="utf8mb4">utf8mb4</option>
          <option value="utf8">utf8</option>
          <option value="latin1">latin1</option>
        </select>
      </div>
      <div class="form-group">
        <label>Collation</label>
        <input [(ngModel)]="form.collation" placeholder="utf8mb4_unicode_ci">
      </div>
      <button class="primary" (click)="create()">Create</button>
    </div>

    <table>
      <thead>
        <tr><th>Name</th><th>Host</th><th>Port</th><th>Username</th><th>Password</th><th>Status</th><th>Actions</th></tr>
      </thead>
      <tbody>
        <tr *ngFor="let db of databases">
          <td>{{ db.status.databaseName || db.name }}</td>
          <td>{{ db.status.host }}</td>
          <td>{{ db.status.port }}</td>
          <td>{{ db.status.username }}</td>
          <td>
            <code *ngIf="db.status.password && editingPasswordId !== db.id">{{ db.status.password }}</code>
            <input *ngIf="editingPasswordId === db.id" [(ngModel)]="newPassword" placeholder="New password (min 8 chars)" style="width:200px">
          </td>
          <td><span class="badge" [ngClass]="db.status.phase === 'Ready' ? 'green' : 'yellow'">{{ db.status.phase }}</span></td>
          <td>
            <button *ngIf="editingPasswordId !== db.id" (click)="startEditPassword(db)">Change Password</button>
            <button *ngIf="editingPasswordId === db.id" class="primary" (click)="savePassword(db)">Save</button>
            <button *ngIf="editingPasswordId === db.id" (click)="cancelEditPassword()">Cancel</button>
            <button class="danger" (click)="remove(db)">Delete</button>
          </td>
        </tr>
      </tbody>
    </table>
  `
})
export class DatabasesComponent implements OnInit {
  databases: Database[] = [];
  showCreate = false;
  form = { name: '', charset: 'utf8mb4', collation: 'utf8mb4_unicode_ci' };
  error = '';
  success = '';
  editingPasswordId: string | null = null;
  newPassword = '';

  pmaUrl = /^\d+\.\d+\.\d+\.\d+$/.test(window.location.hostname)
    ? `http://${window.location.hostname}:30082`
    : `${window.location.protocol}//phpmyadmin.${window.location.hostname}`;

  constructor(private api: ApiService) {}

  ngOnInit(): void { this.load(); }

  load(): void {
    this.api.getDatabases().subscribe({
      next: d => this.databases = d,
      error: () => this.error = 'Failed to load databases.'
    });
  }

  create(): void {
    this.error = ''; this.success = '';
    this.api.createDatabase(this.form).subscribe({
      next: () => { this.success = 'Database created.'; this.showCreate = false; this.load(); },
      error: (e) => this.error = e.error?.error?.message || 'Creation failed.'
    });
  }

  startEditPassword(db: Database): void {
    this.editingPasswordId = db.id;
    this.newPassword = '';
    this.error = '';
    this.success = '';
  }

  cancelEditPassword(): void {
    this.editingPasswordId = null;
    this.newPassword = '';
  }

  savePassword(db: Database): void {
    this.error = ''; this.success = '';
    if (this.newPassword.length < 8) {
      this.error = 'Password must be at least 8 characters.';
      return;
    }
    this.api.updateDatabasePassword(db.id, this.newPassword).subscribe({
      next: () => {
        this.success = 'Password updated.';
        this.editingPasswordId = null;
        this.newPassword = '';
        this.load();
      },
      error: (e) => this.error = e.error?.error?.message || 'Password update failed.'
    });
  }

  remove(db: Database): void {
    if (!confirm(`Delete database "${db.name}"?`)) return;
    this.api.deleteDatabase(db.id).subscribe({
      next: () => { this.success = 'Database deleted.'; this.load(); },
      error: (e) => this.error = e.error?.error?.message || 'Delete failed.'
    });
  }
}
