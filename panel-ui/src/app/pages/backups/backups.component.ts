import { Component, OnInit } from '@angular/core';
import { NgIf, NgFor, NgClass } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from '../../services/api.service';
import { AuthService } from '../../services/auth.service';
import { Backup } from '../../models';

@Component({
  selector: 'app-backups',
  standalone: true,
  imports: [NgIf, NgFor, NgClass, FormsModule],
  template: `
    <div class="page-header">
      <h2>Backups</h2>
      <div>
        <button class="primary" (click)="createBackup()">Manual Backup</button>
        <button *ngIf="auth.isAdmin" (click)="showImport = !showImport">VestaCP Import</button>
      </div>
    </div>

    <div *ngIf="error" class="alert error">{{ error }}</div>
    <div *ngIf="success" class="alert success">{{ success }}</div>

    <!-- VestaCP/HestiaCP import -->
    <div *ngIf="showImport" class="card">
      <h3>Import VestaCP/HestiaCP Backup</h3>
      <div class="form-group">
        <label>Backup Archive (.tar / .tar.gz)</label>
        <input type="file" (change)="onImportFile($event)" accept=".tar,.tar.gz,.tgz">
      </div>
      <button class="primary" (click)="importBackup()">Import</button>
      <button (click)="showImport = false">Cancel</button>
    </div>

    <!-- Restore dialog -->
    <div *ngIf="restoringBackup" class="card">
      <h3>Restore Backup — {{ restoringBackup.createdAt }}</h3>
      <p>Select components to restore:</p>
      <div class="form-group">
        <label><input type="checkbox" [(ngModel)]="restoreOpts.websites"> Website files</label>
      </div>
      <div class="form-group">
        <label><input type="checkbox" [(ngModel)]="restoreOpts.databases"> Databases</label>
      </div>
      <div class="form-group">
        <label><input type="checkbox" [(ngModel)]="restoreOpts.emails"> Email data</label>
      </div>
      <div class="form-group">
        <label><input type="checkbox" [(ngModel)]="restoreOpts.dns"> DNS zones</label>
      </div>
      <button class="primary" (click)="restore()">Restore Selected</button>
      <button (click)="restoreAll()">Restore All</button>
      <button (click)="restoringBackup = null">Cancel</button>
    </div>

    <!-- Backup list -->
    <table>
      <thead>
        <tr><th>Date</th><th>Size</th><th>Destination</th><th>Status</th><th>Actions</th></tr>
      </thead>
      <tbody>
        <tr *ngFor="let b of backups">
          <td>{{ b.createdAt }}</td>
          <td>{{ b.size }}</td>
          <td>{{ b.destination }}</td>
          <td><span class="badge" [ngClass]="b.status === 'Completed' ? 'green' : b.status === 'Failed' ? 'red' : 'yellow'">{{ b.status }}</span></td>
          <td><button (click)="restoringBackup = b; resetRestoreOpts()">Restore</button></td>
        </tr>
      </tbody>
    </table>
  `
})
export class BackupsComponent implements OnInit {
  backups: Backup[] = [];
  showImport = false;
  importFile: File | null = null;
  restoringBackup: Backup | null = null;
  restoreOpts = { websites: true, databases: true, emails: true, dns: true };
  error = '';
  success = '';

  constructor(private api: ApiService, public auth: AuthService) {}

  ngOnInit(): void { this.load(); }

  load(): void {
    this.api.getBackups().subscribe({
      next: b => this.backups = b,
      error: () => this.error = 'Failed to load backups.'
    });
  }

  createBackup(): void {
    this.error = ''; this.success = '';
    this.api.createBackup().subscribe({
      next: () => { this.success = 'Backup started.'; this.load(); },
      error: (e) => this.error = e.error?.error?.message || 'Backup failed.'
    });
  }

  resetRestoreOpts(): void {
    this.restoreOpts = { websites: true, databases: true, emails: true, dns: true };
  }

  restore(): void {
    if (!this.restoringBackup) return;
    this.error = ''; this.success = '';
    const components: string[] = [];
    if (this.restoreOpts.websites) components.push('websites');
    if (this.restoreOpts.databases) components.push('databases');
    if (this.restoreOpts.emails) components.push('emails');
    if (this.restoreOpts.dns) components.push('dns');
    this.api.restoreBackup(this.restoringBackup.id, { components }).subscribe({
      next: () => { this.success = 'Restore started.'; this.restoringBackup = null; },
      error: (e) => this.error = e.error?.error?.message || 'Restore failed.'
    });
  }

  restoreAll(): void {
    if (!this.restoringBackup) return;
    this.error = ''; this.success = '';
    this.api.restoreBackup(this.restoringBackup.id, {}).subscribe({
      next: () => { this.success = 'Full restore started.'; this.restoringBackup = null; },
      error: (e) => this.error = e.error?.error?.message || 'Restore failed.'
    });
  }

  onImportFile(event: Event): void {
    const input = event.target as HTMLInputElement;
    this.importFile = input.files?.[0] || null;
  }

  importBackup(): void {
    if (!this.importFile) { this.error = 'Please select a backup archive.'; return; }
    this.error = ''; this.success = '';
    const fd = new FormData();
    fd.append('archive', this.importFile);
    this.api.importVestaBackup(fd).subscribe({
      next: () => { this.success = 'Import completed.'; this.showImport = false; this.load(); },
      error: (e) => this.error = e.error?.error?.message || 'Import failed.'
    });
  }
}
