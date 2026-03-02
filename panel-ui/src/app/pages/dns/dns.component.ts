import { Component, OnInit } from '@angular/core';
import { NgIf, NgFor } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from '../../services/api.service';
import { DnsZone, DnsRecord } from '../../models';

@Component({
  selector: 'app-dns',
  standalone: true,
  imports: [NgIf, NgFor, FormsModule],
  template: `
    <div class="page-header">
      <h2>DNS Zones</h2>
      <button class="primary" (click)="showCreateZone = !showCreateZone">{{ showCreateZone ? 'Cancel' : 'Create Zone' }}</button>
    </div>

    <div *ngIf="error" class="alert error">{{ error }}</div>
    <div *ngIf="success" class="alert success">{{ success }}</div>

    <div *ngIf="showCreateZone" class="card">
      <h3>Create DNS Zone</h3>
      <div class="form-group">
        <label>Domain</label>
        <input [(ngModel)]="newZoneName" placeholder="example.com">
      </div>
      <button class="primary" (click)="createZone()">Create</button>
    </div>

    <!-- Zone list -->
    <div *ngFor="let z of zones" class="card">
      <div class="page-header">
        <h3>{{ z.name }}</h3>
        <div>
          <button class="primary" (click)="startAddRecord(z)">Add Record</button>
          <button *ngIf="z.secondaryIps" (click)="editSecondary(z)">Secondary DNS</button>
          <button class="danger" (click)="deleteZone(z)">Delete Zone</button>
        </div>
      </div>

      <!-- Add/Edit record form -->
      <div *ngIf="editingZone?.id === z.id && showRecordForm" class="card" style="background:#f9fafb">
        <h4>{{ editingRecord?.id ? 'Edit Record' : 'Add Record' }}</h4>
        <div style="display:grid;grid-template-columns:1fr 1fr 2fr 1fr 1fr auto;gap:8px;align-items:end">
          <div class="form-group"><label>Name</label><input [(ngModel)]="recordForm.name" placeholder="@"></div>
          <div class="form-group">
            <label>Type</label>
            <select [(ngModel)]="recordForm.type">
              <option *ngFor="let t of recordTypes" [value]="t">{{ t }}</option>
            </select>
          </div>
          <div class="form-group"><label>Content</label><input [(ngModel)]="recordForm.content" placeholder="1.2.3.4"></div>
          <div class="form-group"><label>TTL</label><input [(ngModel)]="recordForm.ttl" type="number"></div>
          <div class="form-group" *ngIf="recordForm.type === 'MX' || recordForm.type === 'SRV'">
            <label>Priority</label><input [(ngModel)]="recordForm.priority" type="number">
          </div>
          <div><button class="primary" (click)="saveRecord(z)">Save</button></div>
        </div>
      </div>

      <!-- Records table -->
      <table>
        <thead><tr><th>Name</th><th>Type</th><th>Content</th><th>TTL</th><th>Priority</th><th>Actions</th></tr></thead>
        <tbody>
          <tr *ngFor="let r of z.records">
            <td>{{ r.name }}</td>
            <td><span class="badge blue">{{ r.type }}</span></td>
            <td>{{ r.content }}</td>
            <td>{{ r.ttl }}</td>
            <td>{{ r.priority ?? '—' }}</td>
            <td>
              <button (click)="editRecord(z, r)">Edit</button>
              <button class="danger" (click)="deleteRecord(z, r)">Delete</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  `
})
export class DnsComponent implements OnInit {
  zones: DnsZone[] = [];
  showCreateZone = false;
  newZoneName = '';
  showRecordForm = false;
  editingZone: DnsZone | null = null;
  editingRecord: DnsRecord | null = null;
  recordForm: DnsRecord = { name: '', type: 'A', content: '', ttl: 3600 };
  recordTypes: string[] = ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'SRV', 'NS'];
  error = '';
  success = '';

  constructor(private api: ApiService) {}

  ngOnInit(): void { this.load(); }

  load(): void {
    this.api.getDnsZones().subscribe({
      next: zones => {
        this.zones = zones;
        // Load records for each zone
        zones.forEach(z => {
          z.records = z.records || [];
          this.api.getDnsRecords(z.id).subscribe({
            next: records => {
              z.records = records;
              // Trigger change detection by reassigning the zones array
              this.zones = [...this.zones];
            },
            error: (e) => {
              console.warn('Failed to load records for zone', z.id, e?.error?.error?.message || e?.status);
              z.records = [];
            }
          });
        });
      },
      error: () => this.error = 'Failed to load DNS zones.'
    });
  }

  createZone(): void {
    this.error = ''; this.success = '';
    this.api.createDnsZone({ name: this.newZoneName }).subscribe({
      next: () => { this.success = 'Zone created.'; this.showCreateZone = false; this.newZoneName = ''; this.load(); },
      error: (e) => this.error = e.error?.error?.message || 'Failed to create zone.'
    });
  }

  startAddRecord(z: DnsZone): void {
    this.editingZone = z;
    this.editingRecord = null;
    this.recordForm = { name: '', type: 'A', content: '', ttl: 3600 };
    this.showRecordForm = true;
  }

  editRecord(z: DnsZone, r: DnsRecord): void {
    this.editingZone = z;
    this.editingRecord = r;
    this.recordForm = { ...r };
    this.showRecordForm = true;
  }

  saveRecord(z: DnsZone): void {
    this.error = ''; this.success = '';
    if (this.editingRecord?.name) {
      this.api.updateDnsRecord(z.id, this.recordForm).subscribe({
        next: () => { this.success = 'Record updated.'; this.showRecordForm = false; this.load(); },
        error: (e) => this.error = e.error?.error?.message || 'Failed to update record.'
      });
    } else {
      this.api.createDnsRecord(z.id, this.recordForm).subscribe({
        next: () => { this.success = 'Record created.'; this.showRecordForm = false; this.load(); },
        error: (e) => this.error = e.error?.error?.message || 'Failed to create record.'
      });
    }
  }

  deleteZone(z: DnsZone): void {
    if (!confirm(`Delete zone "${z.name}" and all its records?`)) return;
    this.api.deleteDnsZone(z.id).subscribe({
      next: () => { this.success = 'Zone deleted.'; this.load(); },
      error: (e) => this.error = e.error?.error?.message || 'Failed to delete zone.'
    });
  }

  deleteRecord(z: DnsZone, r: DnsRecord): void {
    if (!confirm(`Delete ${r.type} record "${r.name}"?`)) return;
    this.api.deleteDnsRecord(z.id, r).subscribe({
      next: () => { this.success = 'Record deleted.'; this.load(); },
      error: (e) => this.error = e.error?.error?.message || 'Failed to delete record.'
    });
  }

  editSecondary(_z: DnsZone): void {
    // Secondary DNS config — placeholder for future enhancement
    alert('Secondary DNS configuration coming soon.');
  }
}
