import { Injectable } from '@angular/core';
import { HttpClient, HttpHeaders } from '@angular/common/http';
import { Observable } from 'rxjs';
import { AuthService } from './auth.service';
import {
  User, HostingPlan, Website, Database, EmailAccount,
  DnsZone, DnsRecord, Certificate, Backup, BackupSchedule, HealthStatus
} from '../models';

@Injectable({ providedIn: 'root' })
export class ApiService {
  private base = '/api';

  constructor(private http: HttpClient, private auth: AuthService) {}

  private headers(): HttpHeaders {
    let h = new HttpHeaders({ Authorization: `Bearer ${this.auth.accessToken}` });
    const impersonated = this.auth.impersonatedUser;
    if (impersonated) {
      h = h.set('X-Impersonate-User', impersonated);
    }
    return h;
  }

  // Health
  getHealth(): Observable<HealthStatus> {
    return this.http.get<HealthStatus>(`${this.base}/health`, { headers: this.headers() });
  }

  // Users
  getUsers(): Observable<User[]> {
    return this.http.get<User[]>(`${this.base}/users`, { headers: this.headers() });
  }
  getUser(id: string): Observable<User> {
    return this.http.get<User>(`${this.base}/users/${id}`, { headers: this.headers() });
  }
  createUser(data: Partial<User> & { password: string }): Observable<User> {
    return this.http.post<User>(`${this.base}/users`, data, { headers: this.headers() });
  }
  updateUser(id: string, data: Partial<User>): Observable<User> {
    return this.http.put<User>(`${this.base}/users/${id}`, data, { headers: this.headers() });
  }
  deleteUser(id: string): Observable<void> {
    return this.http.delete<void>(`${this.base}/users/${id}`, { headers: this.headers() });
  }
  changePassword(id: string, password: string): Observable<void> {
    return this.http.put<void>(`${this.base}/users/${id}/password`, { password }, { headers: this.headers() });
  }
  impersonateUser(id: string): Observable<any> {
    return this.http.post<any>(`${this.base}/users/${id}/impersonate`, {}, { headers: this.headers() });
  }
  exitImpersonation(): Observable<any> {
    return this.http.post<any>(`${this.base}/users/impersonate/exit`, {}, { headers: this.headers() });
  }

  // Websites
  getWebsites(): Observable<Website[]> {
    return this.http.get<Website[]>(`${this.base}/websites`, { headers: this.headers() });
  }
  getWebsite(id: string): Observable<Website> {
    return this.http.get<Website>(`${this.base}/websites/${id}`, { headers: this.headers() });
  }
  createWebsite(data: Partial<Website>): Observable<Website> {
    return this.http.post<Website>(`${this.base}/websites`, data, { headers: this.headers() });
  }
  updateWebsite(id: string, data: Partial<Website>): Observable<Website> {
    return this.http.put<Website>(`${this.base}/websites/${id}`, data, { headers: this.headers() });
  }
  deleteWebsite(id: string): Observable<void> {
    return this.http.delete<void>(`${this.base}/websites/${id}`, { headers: this.headers() });
  }
  getWebsiteDomains(id: string): Observable<{ name: string; ssl: { enabled: boolean } }[]> {
    return this.http.get<{ name: string; ssl: { enabled: boolean } }[]>(
      `${this.base}/websites/${id}/domains`, { headers: this.headers() });
  }
  addDomain(websiteId: string, domain: { name: string; ssl: { enabled: boolean; mode: string; issuer?: string; secretName?: string } }): Observable<void> {
    return this.http.post<void>(`${this.base}/websites/${websiteId}/domains`, domain, { headers: this.headers() });
  }
  removeDomain(websiteId: string, domain: string): Observable<void> {
    return this.http.delete<void>(`${this.base}/websites/${websiteId}/domains`, {
      headers: this.headers(), body: { name: domain }
    });
  }
  addAlias(websiteId: string, alias: string): Observable<void> {
    return this.http.post<void>(`${this.base}/websites/${websiteId}/aliases`, { alias }, { headers: this.headers() });
  }
  removeAlias(websiteId: string, alias: string): Observable<void> {
    return this.http.delete<void>(`${this.base}/websites/${websiteId}/aliases`, {
      headers: this.headers(), body: { alias }
    });
  }

  // Databases
  getDatabases(): Observable<Database[]> {
    return this.http.get<Database[]>(`${this.base}/databases`, { headers: this.headers() });
  }
  createDatabase(data: Partial<Database>): Observable<Database> {
    return this.http.post<Database>(`${this.base}/databases`, data, { headers: this.headers() });
  }
  updateDatabasePassword(id: string, password: string): Observable<Database> {
    return this.http.put<Database>(`${this.base}/databases/${id}`, { password }, { headers: this.headers() });
  }
  deleteDatabase(id: string): Observable<void> {
    return this.http.delete<void>(`${this.base}/databases/${id}`, { headers: this.headers() });
  }

  // Email accounts
  getEmailAccounts(): Observable<EmailAccount[]> {
    return this.http.get<EmailAccount[]>(`${this.base}/email-accounts`, { headers: this.headers() });
  }
  createEmailAccount(data: Partial<EmailAccount>): Observable<EmailAccount> {
    return this.http.post<EmailAccount>(`${this.base}/email-accounts`, data, { headers: this.headers() });
  }
  deleteEmailAccount(id: string): Observable<void> {
    return this.http.delete<void>(`${this.base}/email-accounts/${id}`, { headers: this.headers() });
  }

  // Email domains
  getEmailDomains(): Observable<any[]> {
    return this.http.get<any[]>(`${this.base}/email-domains`, { headers: this.headers() });
  }
  createEmailDomain(data: { domain: string; spamFilter: boolean }): Observable<any> {
    return this.http.post<any>(`${this.base}/email-domains`, data, { headers: this.headers() });
  }
  deleteEmailDomain(id: string): Observable<void> {
    return this.http.delete<void>(`${this.base}/email-domains/${id}`, { headers: this.headers() });
  }

  // DNS
  getDnsZones(): Observable<DnsZone[]> {
    return this.http.get<DnsZone[]>(`${this.base}/dns/zones`, { headers: this.headers() });
  }
  createDnsZone(data: { name: string }): Observable<DnsZone> {
    return this.http.post<DnsZone>(`${this.base}/dns/zones`, data, { headers: this.headers() });
  }
  getDnsRecords(zoneId: string): Observable<DnsRecord[]> {
    return this.http.get<DnsRecord[]>(`${this.base}/dns/zones/${zoneId}/records`, { headers: this.headers() });
  }
  createDnsRecord(zoneId: string, record: DnsRecord): Observable<DnsRecord> {
    return this.http.post<DnsRecord>(`${this.base}/dns/zones/${zoneId}/records`, record, { headers: this.headers() });
  }
  updateDnsRecord(zoneId: string, record: DnsRecord): Observable<DnsRecord> {
    return this.http.put<DnsRecord>(
      `${this.base}/dns/zones/${zoneId}/records`, record, { headers: this.headers() });
  }
  deleteDnsRecord(zoneId: string, record: DnsRecord): Observable<void> {
    return this.http.delete<void>(
      `${this.base}/dns/zones/${zoneId}/records`, { headers: this.headers(), body: record });
  }
  deleteDnsZone(zoneId: string): Observable<void> {
    return this.http.delete<void>(`${this.base}/dns/zones/${zoneId}`, { headers: this.headers() });
  }

  // Certificates
  getCertificates(): Observable<Certificate[]> {
    return this.http.get<Certificate[]>(`${this.base}/certificates`, { headers: this.headers() });
  }
  uploadCertificate(data: { name?: string; domain: string; tlsCert: string; tlsKey: string; caCert?: string }): Observable<any> {
    return this.http.post<any>(`${this.base}/certificates/upload`, data, { headers: this.headers() });
  }
  generateSelfSigned(domain: string): Observable<any> {
    return this.http.post<any>(`${this.base}/certificates/self-signed`, { domain }, { headers: this.headers() });
  }
  updateCertificate(name: string, data: { tlsCert: string; tlsKey: string; caCert?: string }): Observable<any> {
    return this.http.put<any>(`${this.base}/certificates/${name}`, data, { headers: this.headers() });
  }
  deleteCertificate(name: string): Observable<void> {
    return this.http.delete<void>(`${this.base}/certificates/${name}`, { headers: this.headers() });
  }

  // Backups
  getBackups(): Observable<Backup[]> {
    return this.http.get<Backup[]>(`${this.base}/backups`, { headers: this.headers() });
  }
  createBackup(): Observable<Backup> {
    return this.http.post<Backup>(`${this.base}/backups`, {}, { headers: this.headers() });
  }
  restoreBackup(id: string, options: { components?: string[] }): Observable<void> {
    return this.http.post<void>(`${this.base}/backups/${id}/restore`, options, { headers: this.headers() });
  }
  getBackupSchedules(): Observable<BackupSchedule[]> {
    return this.http.get<BackupSchedule[]>(`${this.base}/backups/schedules`, { headers: this.headers() });
  }
  createBackupSchedule(data: Partial<BackupSchedule>): Observable<BackupSchedule> {
    return this.http.post<BackupSchedule>(`${this.base}/backups/schedules`, data, { headers: this.headers() });
  }
  deleteBackupSchedule(id: string): Observable<void> {
    return this.http.delete<void>(`${this.base}/backups/schedules/${id}`, { headers: this.headers() });
  }
  importVestaBackup(formData: FormData): Observable<void> {
    return this.http.post<void>(`${this.base}/backups/import`, formData, {
      headers: new HttpHeaders({ Authorization: `Bearer ${this.auth.accessToken}` })
    });
  }

  // Hosting Plans
  getHostingPlans(): Observable<HostingPlan[]> {
    return this.http.get<HostingPlan[]>(`${this.base}/hosting-plans`, { headers: this.headers() });
  }
  createHostingPlan(data: Partial<HostingPlan>): Observable<HostingPlan> {
    return this.http.post<HostingPlan>(`${this.base}/hosting-plans`, data, { headers: this.headers() });
  }
  updateHostingPlan(id: string, data: Partial<HostingPlan>): Observable<HostingPlan> {
    return this.http.put<HostingPlan>(`${this.base}/hosting-plans/${id}`, data, { headers: this.headers() });
  }
  deleteHostingPlan(id: string): Observable<void> {
    return this.http.delete<void>(`${this.base}/hosting-plans/${id}`, { headers: this.headers() });
  }
  assignPlan(planId: string, userId: string): Observable<void> {
    return this.http.post<void>(`${this.base}/hosting-plans/${planId}/assign`, { userId }, { headers: this.headers() });
  }
}
