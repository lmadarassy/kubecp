import { Component, OnInit } from '@angular/core';
import { NgIf, NgFor } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from '../../services/api.service';
import { HostingPlan } from '../../models';

@Component({
  selector: 'app-hosting-plans',
  standalone: true,
  imports: [NgIf, NgFor, FormsModule],
  template: `
    <div class="page-header">
      <h2>Hosting Plans</h2>
      <button class="primary" (click)="showForm = !showForm">{{ showForm ? 'Cancel' : 'Create Plan' }}</button>
    </div>

    <div *ngIf="error" class="alert error">{{ error }}</div>
    <div *ngIf="success" class="alert success">{{ success }}</div>

    <div *ngIf="showForm" class="card">
      <h3>{{ editing ? 'Edit Plan' : 'Create Plan' }}</h3>
      <div class="form-group">
        <label>Display Name</label>
        <input [(ngModel)]="form.displayName" placeholder="Basic Plan">
      </div>
      <div class="grid-form">
        <div class="form-group"><label>Websites</label><input [(ngModel)]="form.websites" type="number"></div>
        <div class="form-group"><label>Databases</label><input [(ngModel)]="form.databases" type="number"></div>
        <div class="form-group"><label>Email Accounts</label><input [(ngModel)]="form.emailAccounts" type="number"></div>
        <div class="form-group"><label>Storage (GB)</label><input [(ngModel)]="form.storageGB" type="number"></div>
        <div class="form-group"><label>CPU (millicores)</label><input [(ngModel)]="form.cpuMillicores" type="number"></div>
        <div class="form-group"><label>Memory (MB)</label><input [(ngModel)]="form.memoryMB" type="number"></div>
      </div>
      <button class="primary" (click)="save()">{{ editing ? 'Update' : 'Create' }}</button>
    </div>

    <table>
      <thead>
        <tr><th>Name</th><th>Websites</th><th>Databases</th><th>Emails</th><th>Storage</th><th>CPU</th><th>Memory</th><th>Actions</th></tr>
      </thead>
      <tbody>
        <tr *ngFor="let p of plans">
          <td>{{ p.displayName }}</td>
          <td>{{ p.limits.websites }}</td>
          <td>{{ p.limits.databases }}</td>
          <td>{{ p.limits.emailAccounts }}</td>
          <td>{{ p.limits.storageGB }} GB</td>
          <td>{{ p.limits.cpuMillicores }}m</td>
          <td>{{ p.limits.memoryMB }} MB</td>
          <td>
            <button (click)="edit(p)">Edit</button>
            <button class="danger" (click)="remove(p)">Delete</button>
          </td>
        </tr>
      </tbody>
    </table>
  `,
  styles: [`
    .grid-form { display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 12px; }
    @media (max-width: 700px) { .grid-form { grid-template-columns: 1fr; } }
  `]
})
export class HostingPlansComponent implements OnInit {
  plans: HostingPlan[] = [];
  showForm = false;
  editing: HostingPlan | null = null;
  form = { displayName: '', websites: 5, databases: 10, emailAccounts: 20, storageGB: 50, cpuMillicores: 2000, memoryMB: 4096 };
  error = '';
  success = '';

  constructor(private api: ApiService) {}

  ngOnInit(): void { this.load(); }

  load(): void {
    this.api.getHostingPlans().subscribe({
      next: p => this.plans = p,
      error: () => this.error = 'Failed to load hosting plans.'
    });
  }

  edit(p: HostingPlan): void {
    this.editing = p;
    this.form = { displayName: p.displayName, ...p.limits };
    this.showForm = true;
  }

  save(): void {
    this.error = ''; this.success = '';
    const data: Partial<HostingPlan> = {
      displayName: this.form.displayName,
      limits: {
        websites: this.form.websites, databases: this.form.databases,
        emailAccounts: this.form.emailAccounts, storageGB: this.form.storageGB,
        cpuMillicores: this.form.cpuMillicores, memoryMB: this.form.memoryMB
      }
    };
    if (this.editing) {
      this.api.updateHostingPlan(this.editing.id, data).subscribe({
        next: () => { this.success = 'Plan updated.'; this.reset(); this.load(); },
        error: (e) => this.error = e.error?.error?.message || 'Update failed.'
      });
    } else {
      this.api.createHostingPlan(data).subscribe({
        next: () => { this.success = 'Plan created.'; this.reset(); this.load(); },
        error: (e) => this.error = e.error?.error?.message || 'Creation failed.'
      });
    }
  }

  remove(p: HostingPlan): void {
    if (!confirm(`Delete plan "${p.displayName}"?`)) return;
    this.api.deleteHostingPlan(p.id).subscribe({
      next: () => { this.success = 'Plan deleted.'; this.load(); },
      error: (e) => this.error = e.error?.error?.message || 'Delete failed.'
    });
  }

  reset(): void { this.showForm = false; this.editing = null; this.form = { displayName: '', websites: 5, databases: 10, emailAccounts: 20, storageGB: 50, cpuMillicores: 2000, memoryMB: 4096 }; }
}
