package main

import (
 "encoding/json"
 "strings"
 "testing"
 "time"

 "fold/models"
)

func TestRegistrationSubjectIdentity(t *testing.T) {
 user,person,holder:=uint(7),uint(7),uint(9)
 for _,tt:=range []struct{name string;r models.Registration;holder uint;valid bool}{
  {"user",models.Registration{UserID:&user},user,true},
  {"household shares numeric id",models.Registration{HouseholdPersonID:&person,RegisteredBy:&holder},holder,true},
  {"neither",models.Registration{},0,false},
  {"both",models.Registration{UserID:&user,HouseholdPersonID:&person,RegisteredBy:&holder},0,false},
  {"missing holder",models.Registration{HouseholdPersonID:&person},0,false},
  {"user has household holder",models.Registration{UserID:&user,RegisteredBy:&holder},0,false},
 } {t.Run(tt.name,func(t *testing.T){s,err:=subjectOf(tt.r);if (err==nil)!=tt.valid {t.Fatalf("subject: %+v %v",s,err)};if tt.valid && s.holderID!=tt.holder {t.Fatalf("holder %d",s.holderID)}})}
}

func TestListedRegistrationSubjects(t *testing.T) {
 app:=testApp(t)
 holder:=newTestUser(t,app,"holder","password1234",models.RoleMember)
 other:=newTestUser(t,app,"other","password1234",models.RoleMember)
 h:=models.Household{Name:"Household",CreatedBy:holder.ID};if err:=app.DB.Create(&h).Error;err!=nil{t.Fatal(err)}
 if err:=app.DB.Create(&models.HouseholdAccount{HouseholdID:h.ID,UserID:holder.ID,Relation:models.HouseholdRelationAdult}).Error;err!=nil{t.Fatal(err)}
 birth:=time.Date(1990,1,1,0,0,0,0,time.UTC)
 person:=models.HouseholdPerson{HouseholdID:h.ID,FirstName:"Mary",Kind:models.HouseholdAdult,BirthDate:&birth,CreatedBy:holder.ID}
 if err:=app.DB.Create(&person).Error;err!=nil{t.Fatal(err)}
 subject,err:=householdSubject(app.DB,person.ID,holder.ID);if err!=nil{t.Fatal(err)}
 if subject.holderID!=holder.ID {t.Fatal("wrong holder")}
 if _,err:=householdSubject(app.DB,person.ID,other.ID);err==nil {t.Fatal("unrelated holder admitted")}
 // A real event supplies foreign keys without going through a fake user.
 admin,gid:=openGroup(t,app,"Subjects")
 eventID,date:=seedCappedEvent(t,app,admin,gid,nil,true)
 var occ models.EventOccurrence;if err:=app.DB.Where("event_id = ? AND original_date = ?",eventID,date).First(&occ).Error;err!=nil{t.Fatal(err)}
 r:=subject.row(occ.ID);r.Status=models.RegGoing;r.AttendanceMode=models.AttendanceInPerson
 if err:=app.DB.Create(&r).Error;err!=nil{t.Fatal(err)}
 assertSeats:=func(want int){t.Helper();got,err:=seatsUsed(app.DB,occ.ID);if err!=nil || got!=want{t.Fatalf("seats %d want %d: %v",got,want,err)}}
 assertSeats(1)
 if err:=app.DB.Model(holder).Update("status",models.UserDisabled).Error;err!=nil{t.Fatal(err)};assertSeats(0)
 app.DB.Model(holder).Update("status",models.UserActive);assertSeats(1)
 app.DB.Model(&person).Update("converted_user_id",other.ID);assertSeats(0)
 app.DB.Model(&person).Update("converted_user_id",nil);assertSeats(1)
 app.DB.Delete(&person);assertSeats(0)
}

func TestHouseholdAttendeeCareProjection(t *testing.T) {
 holderID,personID:=uint(1),uint(2)
 start:=time.Date(2026,11,1,18,0,0,0,time.UTC)
 birth:=time.Date(2020,11,2,0,0,0,0,time.UTC)
 person:=models.HouseholdPerson{ID:personID,HouseholdID:3,FirstName:"Kit",Kind:models.HouseholdChild,BirthDate:&birth,CareNotes:"Private care\n"+strings.Repeat("long",100)}
 holder:=models.User{ID:holderID,Name:"John Smith",Status:models.UserActive}
 manager:=models.User{ID:4,Name:"Host",Status:models.UserActive}
 f:=subjectFacts{people:map[uint]*models.HouseholdPerson{personID:&person},links:map[uint][]models.GuardianLink{personID:{{PersonID:&personID,GuardianUserID:holderID,Status:models.GuardianConfirmed}}},households:map[uint]models.Household{3:{ID:3,Name:"Leader household",Address:"Never expose address"}},users:map[uint]*models.User{holderID:&holder},accounts:map[uint]models.HouseholdAccount{holderID:{HouseholdID:3,UserID:holderID,Relation:models.HouseholdRelationGuardian}}}
 r:=models.Registration{ID:10,HouseholdPersonID:&personID,RegisteredBy:&holderID,Status:models.RegGoing,AttendanceMode:models.AttendanceInPerson,ChildcareRequested:true}
 o:=models.EventOccurrence{ID:5,EventID:6,Event:models.Event{TZID:"UTC"},StartsAt:start,EndsAt:start.Add(time.Hour),Status:models.OccurrenceScheduled}
 for _,tt:=range []struct{name string;now time.Time;status string;manager bool;viewer *models.User;visible,care bool}{
  {"manager exact opening",start.Add(-24*time.Hour),models.RegGoing,true,&manager,true,true},
  {"manager exact closing",start.Add(25*time.Hour),models.RegGoing,true,&manager,true,true},
  {"manager too early",start.Add(-24*time.Hour-time.Nanosecond),models.RegGoing,true,&manager,true,false},
  {"manager too late",start.Add(25*time.Hour+time.Nanosecond),models.RegGoing,true,&manager,true,false},
  {"wrong event manager",start,models.RegGoing,false,&manager,false,false},
  {"waitlisted",start,models.RegWaitlisted,true,&manager,true,false},
  {"maybe",start,models.RegMaybe,true,&manager,true,false},
  {"removed",start,models.RegRemoved,true,&manager,true,false},
  {"guardian outside window",start.Add(-40*time.Hour),models.RegGoing,false,&holder,true,true},
  {"anonymous",start,models.RegGoing,false,nil,false,false},
 } {t.Run(tt.name,func(t *testing.T){r.Status=tt.status;dto:=f.attendee(r,o,tt.viewer,tt.manager,true,tt.now);if (dto!=nil)!=tt.visible {t.Fatalf("visibility: %+v",dto)};if dto==nil{return};hp:=dto.HouseholdPerson;if (hp.CareNotes!=nil)!=tt.care{t.Fatalf("care: %+v",hp)};if hp.Age==nil || *hp.Age!=5 || dto.UserID!=nil || hp.RegisteredBy.Name!="John Smith" {t.Fatalf("identity or age: %+v",dto)};raw,_:=json.Marshal(dto);for _,secret:=range []string{"2020-11-02","Never expose address"}{if strings.Contains(string(raw),secret){t.Fatalf("private data %s",raw)}}})}
}
